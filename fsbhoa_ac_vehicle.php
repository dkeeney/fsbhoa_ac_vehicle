<?php
/**
 * Plugin Name: FSBHOA AC Vehicle
 * Description: Vehicle lane hardware (loop sensors, LPR cameras, DoorKing live stream). Its vehicle_service combines their inputs into one event per vehicle and posts it to core's vehicle log.
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
require_once FSBHOA_AC_VEHICLE_DIR . 'includes/class-fsbhoa-vehicle-video.php';
if ( defined( 'WP_CLI' ) && WP_CLI ) {
	require_once FSBHOA_AC_VEHICLE_DIR . 'includes/class-fsbhoa-vehicle-cli.php'; // settings export and import
}

/**
 * Initialize plugin components.
 */
FSBHOA_Vehicle_Settings::get_instance();
FSBHOA_Vehicle_Video::init(); // the live monitor's Video button: the NVR's recordings

/**
 * Core owns the vehicle log, its REST routes, the monitor column and photo retention.
 * This plugin supplies the events, so turn the column on.
 */
add_filter( 'fsbhoa_monitor_show_vehicle_column', '__return_true' );

/**
 * Core's live monitor offers a button per lane over the vehicle column: the entrance lanes,
 * then the exit lanes, by name, as the lane tables list them.
 */
add_filter(
	'fsbhoa_monitor_vehicle_lanes',
	function ( $lanes ) {
		$options = FSBHOA_Vehicle_Settings::get_options();
		foreach ( array( 'gates', 'exit_lanes' ) as $table ) {
			foreach ( (array) $options[ $table ] as $lane ) {
				if ( ! empty( $lane['name'] ) ) {
					$lanes[] = $lane['name'];
				}
			}
		}
		return $lanes;
	}
);

/**
 * One-time handoff to core (2026-10-13 plugin split): copy this plugin's photo retention
 * setting to core's, and remove this plugin's old daily purge. Runs once per site.
 */
add_action( 'init', 'fsbhoa_vehicle_hand_off_to_core' );
function fsbhoa_vehicle_hand_off_to_core() {
	if ( get_option( 'fsbhoa_vehicle_core_handoff_done' ) ) {
		return;
	}
	if ( ! class_exists( 'Fsbhoa_Vehicle_Log_Retention' ) ) {
		return; // Core without the vehicle log; try again once it's updated
	}

	$stored = get_option( FSBHOA_Vehicle_Settings::OPTION_NAME, array() );
	if ( isset( $stored['image_retention_days'] ) && false === get_option( Fsbhoa_Vehicle_Log_Retention::OPTION_NAME ) ) {
		$days = Fsbhoa_Vehicle_Log_Retention::sanitize_days( $stored['image_retention_days'] );
		update_option( Fsbhoa_Vehicle_Log_Retention::OPTION_NAME, $days );
		error_log( sprintf( 'FSBHOA Vehicle: photo retention (%d days) moved to core\'s Live Monitor settings.', $days ) );
	}
	wp_clear_scheduled_hook( 'fsbhoa_vehicle_purge_images' );

	update_option( 'fsbhoa_vehicle_core_handoff_done', 1 );
}

/**
 * Register Vehicle Daemon on the Core System Status dashboard.
 */
add_filter( 'fsbhoa_system_services', 'fsbhoa_vehicle_register_system_service' );
function fsbhoa_vehicle_register_system_service( $services ) {
    $services['fsbhoa_vehicle'] = 'Vehicle Access Service';
    return $services;
}
