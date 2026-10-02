<?php
/**
 * Plugin Name: FSBHOA AC Vehicle
 * Description: API endpoints and UI display for correlated vehicle tracking events.
 * Version: 1.0.0
 * Author: David E. Keeney
 * Text Domain: fsbhoa-ac-vehicle
 */

if ( ! defined( 'ABSPATH' ) ) {
	exit;
}

define( 'FSBHOA_AC_VEHICLE_VERSION', '1.0.0' );
define( 'FSBHOA_AC_VEHICLE_DIR', plugin_dir_path( __FILE__ ) );
define( 'FSBHOA_AC_VEHICLE_URL', plugin_dir_url( __FILE__ ) );

// Load Settings Class
require_once FSBHOA_AC_VEHICLE_DIR . 'includes/class-fsbhoa-vehicle-settings.php';

/**
 * Initialize plugin components.
 */
FSBHOA_Vehicle_Settings::get_instance();


/**
 * Register Vehicle Daemon on the Core System Status dashboard.
 */
add_filter( 'fsbhoa_system_services', 'fsbhoa_vehicle_register_system_service' );
function fsbhoa_vehicle_register_system_service( $services ) {
    $services['fsbhoa_vehicle'] = 'Vehicle Access Service';
    return $services;
}

/**
 * Register REST API Endpoints
 */
add_action( 'rest_api_init', 'fsbhoa_ac_vehicle_register_endpoints' );
function fsbhoa_ac_vehicle_register_endpoints() {
	// Endpoint 1: Ingest correlated event from vehicle_service
	register_rest_route( 'fsbhoa/v1', '/vehicle-event', array(
		'methods'           => 'POST',
		'callback'         => 'fsbhoa_ac_vehicle_ingest_event',
		'permission_callback' => 'fsbhoa_ac_vehicle_verify_ingest_perms',
	) );

	// Endpoint 2: Serve binary JPEG image
	register_rest_route( 'fsbhoa/v1', '/vehicle-image/(?P<id>\d+)', array(
		'methods'           => 'GET',
		'callback'         => 'fsbhoa_ac_vehicle_serve_image',
		'permission_callback' => '__return_true',
	) );

	// Endpoint 3: Poll recent events for UI
	register_rest_route( 'fsbhoa/v1', '/vehicle-recent', array(
		'methods'           => 'GET',
		'callback'         => 'fsbhoa_ac_vehicle_get_recent',
		'permission_callback' => '__return_true',
	) );
}

/**
 * Verify Ingestion Permissions (IP + Shared Secret)
 */
function fsbhoa_ac_vehicle_verify_ingest_perms( WP_REST_Request $request ) {
	$options = FSBHOA_Vehicle_Settings::get_options();

	// 1. IP Whitelist Validation
	if ( ! empty( $options['allowed_daemon_ips'] ) ) {
		$allowed = array_map( 'trim', explode( ',', $options['allowed_daemon_ips'] ) );
		$remote = ! empty( $_SERVER['REMOTE_ADDR'] ) ? $_SERVER['REMOTE_ADDR'] : '';
		if ( ! in_array( $remote, $allowed, true ) ) {
			return new WP_Error( 'forbidden_ip', 'IP not authorized', array( 'status' => 403 ) );
		}
	}

	// 2. Shared Secret Validation
	if ( ! empty( $options['ingest_secret'] ) ) {
		$token = $request->get_header( 'x_fsbhoa_secret' );
		if ( empty( $token ) ) {
			$auth = $request->get_header( 'authorization' );
			if ( $auth && preg_match( '/Bearer\s+(.+)/i', $auth, $m ) ) {
				$token = $m[1];
			}
		}
		if ( ! hash_equals( $options['ingest_secret'], (string) $token ) ) {
			return new WP_Error( 'unauthorized', 'Invalid ingest secret', array( 'status' => 401 ) );
		}
	}

	return true;
}

/**
 * Ingest Event (Maps to ac_vehicle_log)
 */
function fsbhoa_ac_vehicle_ingest_event( WP_REST_Request $request ) {
	global $wpdb;
	$table = 'ac_vehicle_log';

	$params = $request->get_json_params();
	if ( empty( $params ) ) {
		return new WP_Error( 'invalid_json', 'No JSON payload supplied', array( 'status' => 400 ) );
	}

	$context_blob = ! empty( $params['context_image_b64'] ) ? base64_decode( $params['context_image_b64'] ) : null;
	$lpr_blob     = ! empty( $params['lpr_image_b64'] ) ? base64_decode( $params['lpr_image_b64'] ) : null;

	$timestamp = ! empty( $params['event_timestamp'] ) ? sanitize_text_field( $params['event_timestamp'] ) : current_time( 'mysql', 1 );

	$confidence = isset( $params['lpr_confidence'] ) ? absint( $params['lpr_confidence'] ) : null;
	$raw_details = ! empty( $params['raw_details'] ) ? wp_json_encode( $params['raw_details'] ) : null;

	$data = array(
		'event_timestamp'    => $timestamp,
		'gate_identifier'    => sanitize_text_field( $params['gate_identifier'] ?? '' ),
		'auth_id'           => sanitize_text_field( $params['auth_id'] ?? '' ),
		'lpr_plate_string'   => sanitize_text_field( $params['lpr_plate'] ?? '' ),
		'lpr_confidence'   => $confidence,
		'is_circumvention'   => ! empty( $params['is_circumvention'] ) ? 1 : 0,
		'context_image_data' => $context_blob,
		'lpr_image_data'     => $lpr_blob,
		'raw_details'         => $raw_details,
	);

	$formats = array( '%s', '%s', '%s', '%s', '%d', '%d', '%s', '%s', '%s' );
	$inserted = $wpdb->insert( $table, $data, $formats );

	if ( false === $inserted ) {
		return new WP_Error( 'db_insert_error', $wpdb->last_error, array( 'status' => 500 ) );
	}

	return rest_ensure_response( array(
		'success'        => true,
		'vehicle_log_id' => $wpdb->insert_id,
	) );
}

/**
 * Serve Binary JPEG Image
 */
function fsbhoa_ac_vehicle_serve_image( WP_REST_Request $request ) {
	global $wpdb;
	$log_id = absint( $request['id'] );
	$type   = $request->get_param( 'type' );

	$column = ( 'lpr' === $type ) ? 'lpr_image_data' : 'context_image_data';
	$table  = 'ac_vehicle_log';

	$image_data = $wpdb->get_var( $wpdb->prepare(
		"SELECT {$column} FROM {$table} WHERE vehicle_log_id = %d",
		$log_id
	) );

	if ( empty( $image_data ) ) {
		status_header( 404 );
		nocache_headers();
		exit;
	}

	header( 'Content-Type: image/jpeg' );
	header( 'Content-Length: ' . strlen( $image_data ) );
	header( 'Cache-Control: public, max-age=300' );
	echo $image_data;
	exit;
}

/**
 * Deliver Lightweight JSON for UI Polling
 */
function fsbhoa_ac_vehicle_get_recent( WP_REST_Request $request ) {
	global $wpdb;
	$table = 'ac_vehicle_log';
	$since = absint( $request->get_param( 'since' ) );

	if ( $since > 0 ) {
		$query = $wpdb->prepare(
			"SELECT vehicle_log_id, event_timestamp, gate_identifier, auth_id, lpr_plate_string, lpr_confidence, is_circumvention,
			       (context_image_data IS NOT NULL) AS has_context_img,
			       (lpr_image_data IS NOT NULL) AS has_lpr_img
			 FROM {$table}
			 WHERE vehicle_log_id > %d
			 ORDER BY vehicle_log_id ASC
			 LIMIT 50",
			$since
		);
	} else {
		$query = "SELECT vehicle_log_id, event_timestamp, gate_identifier, auth_id, lpr_plate_string, lpr_confidence, is_circumvention,
			       (context_image_data IS NOT NULL) AS has_context_img,
			       (lpr_image_data IS NOT NULL) AS has_lpr_img
			 FROM {$table}
			 ORDER BY vehicle_log_id DESC
			 LIMIT 20";
	}

	$results = $wpdb->get_results( $query, ARRAY_A );

	return rest_ensure_response( ! empty( $results ) ? $results : array() );
}

add_action( 'wp_enqueue_scripts', 'fsbhoa_vehicle_enqueue_monitor_assets', 20 );
function fsbhoa_vehicle_enqueue_monitor_assets() {
    global $post;
    if ( $post instanceof WP_Post && has_shortcode( $post->post_content, 'fsbhoa_live_monitor' ) ) {
        wp_enqueue_script(
            'fsbhoa-vehicle-monitor-js',
            FSBHOA_AC_VEHICLE_URL . 'assets/js/fsbhoa-vehicle-monitor.js',
            array( 'fsbhoa-live-monitor-script' ),
            FSBHOA_AC_VEHICLE_VERSION,
            true
        );
    }
}




