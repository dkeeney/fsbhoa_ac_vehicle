<?php
/**
 * Recorded video for core's live monitor: each vehicle card's Video button plays the NVR's
 * recording of the lane's cameras from just before the card's time (CLAUDE.md, "Video from
 * the NVR").
 *
 * Core draws the button and the player for the lanes this plugin lists through the
 * fsbhoa_monitor_vehicle_video filter. GET /fsbhoa/v1/vehicle-video (a logged-in admin) passes
 * the video on from vehicle_service's /video on this machine, which plays it from the NVR with
 * the NVR settings. Nothing here talks to the NVR.
 *
 * @package Fsbhoa_Ac_Vehicle
 */

if ( ! defined( 'ABSPATH' ) ) {
	exit;
}

class FSBHOA_Vehicle_Video {

	/** The cameras a lane's video offers, in the lane table's keys, with their labels, in order */
	const CAMERAS = array(
		'overview_ip' => 'Overview',
		'keypad_ip'   => 'Keypad',
		'arm_ip'      => 'Arm',
		'lpr_ip'      => 'Plate',
	);

	public static function init() {
		add_filter( 'fsbhoa_monitor_vehicle_video', array( __CLASS__, 'monitor_video' ) );
		add_action( 'rest_api_init', array( __CLASS__, 'register_routes' ) );
	}

	/** Whether the NVR settings are all set */
	private static function configured( $options ) {
		return '' !== (string) ( $options['nvr_url'] ?? '' )
			&& '' !== (string) ( $options['nvr_user'] ?? '' )
			&& '' !== (string) ( $options['nvr_password'] ?? '' );
	}

	/** Each lane's cameras: lane name => array( array( 'id' => address, 'label' => ... ), ... ) */
	private static function lane_cameras( $options ) {
		$lanes = array();
		foreach ( array( 'gates', 'exit_lanes' ) as $table ) {
			foreach ( (array) ( $options[ $table ] ?? array() ) as $lane ) {
				$name = (string) ( $lane['name'] ?? '' );
				if ( '' === $name ) {
					continue;
				}
				foreach ( self::CAMERAS as $key => $label ) {
					$ip = trim( (string) ( $lane[ $key ] ?? '' ) );
					if ( '' !== $ip ) {
						$lanes[ $name ][] = array( 'id' => $ip, 'label' => $label );
					}
				}
			}
		}
		return $lanes;
	}

	/** For core's monitor: where to get video, and each lane's cameras. Nothing without an NVR. */
	public static function monitor_video( $video ) {
		$options = FSBHOA_Vehicle_Settings::get_options();
		if ( ! self::configured( $options ) ) {
			return $video;
		}
		return array(
			'url'   => rest_url( 'fsbhoa/v1/vehicle-video' ),
			'lanes' => self::lane_cameras( $options ),
		);
	}

	public static function register_routes() {
		register_rest_route(
			'fsbhoa/v1',
			'/vehicle-video',
			array(
				'methods'             => 'GET',
				'callback'            => array( __CLASS__, 'stream' ),
				'permission_callback' => function () {
					return current_user_can( 'manage_options' );
				},
			)
		);
	}

	/**
	 * ?camera=<address>&at=<YYYY-MM-DD HH:MM:SS>&before=<s>&length=<s>&quality=sharp: streams
	 * the MP4 from vehicle_service as it arrives, and stops it when the viewer goes away.
	 */
	public static function stream( WP_REST_Request $request ) {
		$options = FSBHOA_Vehicle_Settings::get_options();
		$camera  = trim( (string) $request->get_param( 'camera' ) );
		$known   = false;
		foreach ( self::lane_cameras( $options ) as $cameras ) {
			foreach ( $cameras as $c ) {
				$known = $known || $c['id'] === $camera;
			}
		}
		if ( ! self::configured( $options ) || ! $known ) {
			return new WP_Error( 'fsbhoa_vehicle_video', 'No NVR set, or not a lane camera.', array( 'status' => 400 ) );
		}
		$query = array(
			'camera'  => $camera,
			'at'      => substr( (string) $request->get_param( 'at' ), 0, 19 ),
			'before'  => (float) ( $request->get_param( 'before' ) ?? 15 ),
			'length'  => (float) ( $request->get_param( 'length' ) ?? 60 ),
			'quality' => 'sharp' === $request->get_param( 'quality' ) ? 'sharp' : '',
		);
		$url = sprintf( 'http://127.0.0.1:%d/video?%s', absint( $options['daemon_port'] ?? 8088 ), http_build_query( $query ) );

		// Send the video as it comes, not as a REST response
		while ( ob_get_level() > 0 ) {
			ob_end_clean();
		}
		set_time_limit( 0 );
		ignore_user_abort( false );
		$status  = 0;
		$started = false;
		$error   = '';
		$ch      = curl_init( $url );
		curl_setopt_array(
			$ch,
			array(
				CURLOPT_CONNECTTIMEOUT => 5,
				CURLOPT_TIMEOUT        => 900,
				CURLOPT_HEADERFUNCTION => function ( $ch, $line ) use ( &$status ) {
					if ( preg_match( '#^HTTP/\S+ (\d{3})#', $line, $m ) ) {
						$status = (int) $m[1];
					}
					return strlen( $line );
				},
				CURLOPT_WRITEFUNCTION  => function ( $ch, $data ) use ( &$status, &$started, &$error ) {
					if ( 200 !== $status ) {
						$error .= $data; // vehicle_service's reason
						return strlen( $data );
					}
					if ( ! $started ) {
						$started = true;
						header( 'Content-Type: video/mp4' );
						header( 'Cache-Control: no-store' );
						header( 'X-Accel-Buffering: no' );
					}
					echo $data; // phpcs:ignore WordPress.Security.EscapeOutput -- video bytes
					flush();
					return connection_aborted() ? 0 : strlen( $data ); // 0 stops the transfer
				},
			)
		);
		curl_exec( $ch );
		$curl_error = curl_error( $ch );
		curl_close( $ch );
		if ( ! $started ) {
			$reason = trim( '' !== $error ? $error : $curl_error );
			error_log( 'FSBHOA Vehicle: video for ' . $camera . ' failed: ' . $reason );
			status_header( 502 );
			header( 'Content-Type: text/plain; charset=utf-8' );
			echo esc_html( '' !== $reason ? $reason : 'vehicle_service did not answer' );
		}
		exit;
	}
}
