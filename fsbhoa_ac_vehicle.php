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

add_action( 'wp_enqueue_scripts', 'fsbhoa_vehicle_enqueue_monitor_assets', 30 );
add_action( 'admin_enqueue_scripts', 'fsbhoa_vehicle_enqueue_monitor_assets', 30 );
function fsbhoa_vehicle_enqueue_monitor_assets() {
    global $post;

    $load = false;
    if ( is_admin() ) {
        if ( isset( $_GET['page'] ) && strpos( sanitize_text_field( wp_unslash($_GET['page'] ) ), 'monitor' ) !== false ) {
            $load = true;
        }
    } elseif ( $post instanceof WP_Post ) {
        if ( has_shortcode( $post->post_content, 'fsbhoa_live_monitor' ) || strpos($post->post_content, 'fsbhoa_live_monitor' ) !== false ) {
            $load = true;
        }
    }

    if ( $load ) {
        wp_enqueue_script(
            'fsbhoa-vehicle-monitor-js',
            FSBHOA_AC_VEHICLE_URL . 'assets/js/fsbhoa-vehicle-monitor.js',
            array(), // No strict dependency handle
            FSBHOA_AC_VEHICLE_VERSION . '.' . time(),
            true
        );
    }
}


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

	// event_timestamp is local time (site time zone), like core's ac_access_log.
	$timestamp = current_time( 'mysql' );
	if ( ! empty( $params['event_timestamp'] ) ) {
		$sent = sanitize_text_field( $params['event_timestamp'] );
		if ( preg_match( '/^\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2}(\.\d{1,6})?$/', $sent ) ) {
			$timestamp = $sent;
		} else {
			error_log( 'FSBHOA Vehicle: ignoring malformed event_timestamp ' . $sent );
		}
	}

	$confidence = isset( $params['lpr_confidence'] ) ? absint( $params['lpr_confidence'] ) : null;
	$raw_details = ! empty( $params['raw_details'] ) ? wp_json_encode( $params['raw_details'] ) : null;

	$data = array(
		'event_timestamp'    => $timestamp,
		'gate_identifier'    => sanitize_text_field( $params['gate_identifier'] ?? '' ),
		'auth_id'           => sanitize_text_field( $params['auth_id'] ?? '' ),
        'auth_type'          => sanitize_text_field( $params['auth_type'] ?? '' ),
		'lpr_plate_string'   => sanitize_text_field( $params['lpr_plate'] ?? '' ),
		'lpr_confidence'   => $confidence,
		'is_circumvention'   => ! empty( $params['is_circumvention'] ) ? 1 : 0,
		'context_image_data' => $context_blob,
		'lpr_image_data'     => $lpr_blob,
		'raw_details'         => $raw_details,
	);

	$formats = array( '%s', '%s', '%s', '%s', '%s', '%d', '%d', '%s', '%s', '%s' );
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
 * Deliver Lightweight JSON for UI Polling with Cardholder Resolution (Last 24 Hours)
 */
function fsbhoa_ac_vehicle_get_recent( WP_REST_Request $request ) {
        global $wpdb;
        $table = 'ac_vehicle_log';
        $since = absint($request->get_param( 'since' ) );

        $select_fields = "
                vl.vehicle_log_id, 
                vl.event_timestamp, 
                vl.gate_identifier, 
                vl.auth_id, 
                vl.auth_type,
                vl.lpr_plate_string, 
                vl.lpr_confidence, 
                vl.is_circumvention,
                (vl.context_image_data IS NOT NULL) AS has_context_img,
                (vl.lpr_image_data IS NOT NULL) AS has_lpr_img,
                COALESCE(
                    CASE WHEN vl.auth_type = 'DK_WINDSHIELD' THEN NULLIF(h.primary_cardholder_id, 0) END,
                    NULLIF(cred.cardholder_id, 0),
                    h.primary_cardholder_id,
                    0
                ) AS cardholder_id,
                COALESCE(
                    CASE WHEN vl.auth_type = 'DK_WINDSHIELD' THEN NULLIF(TRIM(CONCAT(c_primary.first_name, ' ', c_primary.last_name)), '') END,
                    NULLIF(TRIM(CONCAT(c_direct.first_name, ' ', c_direct.last_name)), ''),
                    NULLIF(TRIM(CONCAT(c_primary.first_name, ' ', c_primary.last_name)), ''),
                    ''
                ) AS cardholder_name,
                (SELECT COUNT(*) FROM ac_credentials cnt
                  WHERE cnt.credential_type = vl.auth_type
                    AND cnt.credential_value = vl.auth_id
                    AND cnt.status = 'active') AS credential_matches
        ";

        // A credential value can belong to more than one credential (shared household PINs,
        // reissued tags). Join only the best match so each event returns one row: an active
        // credential of an active cardholder first, then the oldest.
        $joins = "
                LEFT JOIN ac_credentials cred
                    ON cred.id = (
                        SELECT c2.id
                          FROM ac_credentials c2
                          LEFT JOIN ac_cardholders ch2 ON ch2.id = c2.cardholder_id
                         WHERE c2.credential_type = vl.auth_type
                           AND c2.credential_value = vl.auth_id
                         ORDER BY (c2.status = 'active') DESC,
                                  (ch2.cardholder_status = 'active') DESC,
                                  c2.id ASC
                         LIMIT 1
                    )
                LEFT JOIN ac_cardholders c_direct
                    ON c_direct.id = cred.cardholder_id
                LEFT JOIN ac_vehicles v 
                    ON v.vehicle_id = cred.vehicle_id
                LEFT JOIN ac_households h 
                    ON h.household_id = v.household_id
                LEFT JOIN ac_cardholders c_primary 
                    ON c_primary.id = h.primary_cardholder_id
        ";

        if ( $since > 0 ) {
                $query = $wpdb->prepare(
                        "SELECT " . $select_fields . "
                         FROM " . $table . " vl
                         " . $joins . "
                         WHERE vl.vehicle_log_id > %d
                         ORDER BY vl.vehicle_log_id ASC
                         LIMIT 100",
                        $since
                );
        } else {
                $query = "SELECT " .$select_fields . "
                          FROM " . $table . " vl
                          " . $joins . "
                          WHERE vl.event_timestamp >= (NOW() - INTERVAL 24 HOUR)
                          ORDER BY vl.event_timestamp DESC
                          LIMIT 200";
        }

        $results = $wpdb->get_results( $query, ARRAY_A );

        if ( ! empty( $wpdb->last_error ) ) {
                error_log( 'FSBHOA Vehicle: vehicle-recent query failed: ' . $wpdb->last_error . ' -- ' . $wpdb->last_query );
                return new WP_Error( 'db_error', 'Could not load vehicle events', array( 'status' => 500 ) );
        }

        return rest_ensure_response( ! empty( $results ) ? $results : array() );
}




