<?php
/**
 * wp-cli commands for moving the vehicle settings between servers (2026-10-30, for the move to
 * production): wp_options isn't copied when the testbed is refreshed or production deployed, and
 * the lane tables, motion regions and count zones are a lot to retype.
 *
 *   wp fsbhoa-vehicle settings-export <file>
 *   wp fsbhoa-vehicle settings-import <file> [--dry-run] [--yes]
 *
 * The export holds every vehicle setting except the NVR password (passwords are never copied:
 * the import keeps the one this server has, so enter it on the settings page). The import goes
 * through the settings page's own checks (sanitize_settings), which also writes
 * vehicle_service.json; what belongs to this server is kept or made this server's: the RAM
 * Stream File becomes this server's Live_<Environment> path when the file's names the other
 * environment.
 */

if ( ! defined( 'ABSPATH' ) ) {
	exit;
}

class FSBHOA_Vehicle_CLI {

	const FORMAT = 'fsbhoa_vehicle_settings';

	/** Settings never written to the export. */
	const NOT_EXPORTED = array( 'nvr_password' );

	/**
	 * Writes the vehicle settings to a JSON file (mode 0600), without the NVR password.
	 *
	 * ## OPTIONS
	 *
	 * <file>
	 * : Where to write the export.
	 *
	 * @subcommand settings-export
	 */
	public function settings_export( $args ) {
		$settings = array_diff_key( FSBHOA_Vehicle_Settings::get_options(), array_flip( self::NOT_EXPORTED ) );
		$export   = array(
			'format'      => self::FORMAT,
			'from_host'   => wp_parse_url( home_url(), PHP_URL_HOST ),
			'environment' => defined( 'FSBHOA_AC_ENVIRONMENT' ) ? (string) FSBHOA_AC_ENVIRONMENT : '',
			'exported_at' => current_time( 'c' ),
			'settings'    => $settings,
		);
		$file = $args[0];
		$json = json_encode( $export, JSON_PRETTY_PRINT | JSON_UNESCAPED_SLASHES );
		$old  = umask( 0077 );
		$ok   = false !== $json && false !== file_put_contents( $file, $json . "\n" );
		umask( $old );
		if ( ! $ok ) {
			WP_CLI::error( "Could not write $file." );
		}
		chmod( $file, 0600 );
		WP_CLI::success( sprintf( 'Wrote %s: %d lanes, %d exit lanes, %d motion regions, %d count zones. The NVR password is not in it.', $file, count( $settings['gates'] ), count( $settings['exit_lanes'] ), count( $settings['motion_regions'] ), count( $settings['count_zones'] ) ) );
	}

	/**
	 * Replaces this server's vehicle settings with an export's, through the settings page's checks.
	 *
	 * Lists what changes first. The NVR password stays this server's. Remove what this server
	 * shouldn't have (such as the testbed's Workbench lane) from the file before importing, or on
	 * the settings page after. Restart vehicle_service afterwards for the settings it reads only at
	 * start (the port, the LPR Upload Folder).
	 *
	 * ## OPTIONS
	 *
	 * <file>
	 * : An export made by settings-export.
	 *
	 * [--dry-run]
	 * : Only list what would change. Writes nothing (not even vehicle_service.json).
	 *
	 * [--yes]
	 * : Don't ask before importing.
	 *
	 * @subcommand settings-import
	 */
	public function settings_import( $args, $assoc_args ) {
		$file = $args[0];
		$data = json_decode( (string) @file_get_contents( $file ), true );
		if ( ! is_array( $data ) || self::FORMAT !== ( $data['format'] ?? '' ) || ! is_array( $data['settings'] ?? null ) ) {
			WP_CLI::error( "$file is not a vehicle settings export." );
		}
		$env = defined( 'FSBHOA_AC_ENVIRONMENT' ) ? strtolower( trim( (string) FSBHOA_AC_ENVIRONMENT ) ) : '';
		if ( ! in_array( $env, array( 'testbed', 'production' ), true ) ) {
			WP_CLI::error( 'FSBHOA_AC_ENVIRONMENT is not set to testbed or production in wp-config.php; set it before importing.' );
		}

		$current  = FSBHOA_Vehicle_Settings::get_options();
		$input    = array_intersect_key( $data['settings'], FSBHOA_Vehicle_Settings::get_defaults() );
		$input    = array_diff_key( $input, array_flip( self::NOT_EXPORTED ) ); // blank: keeps this server's
		$from_env = strtolower( (string) ( $data['environment'] ?? '' ) );
		WP_CLI::log( sprintf( 'From %s (%s), exported %s, into this %s server.', $data['from_host'] ?? '?', $from_env ? $from_env : 'no environment', $data['exported_at'] ?? '?', $env ) );

		// The RAM Stream File names its server's environment; the other server's is replaced by this one's
		$ram = (string) ( $input['ram_file'] ?? '' );
		if ( '' !== $ram && false === strpos( strtolower( basename( dirname( $ram ) ) ), $env ) ) {
			$input['ram_file'] = FSBHOA_Vehicle_Settings::default_ram_file();
			WP_CLI::log( sprintf( 'RAM Stream File: %s is the other environment\'s; using %s.', $ram, $input['ram_file'] ) );
		}

		$changed = 0;
		foreach ( $input as $key => $value ) {
			if ( wp_json_encode( $value ) === wp_json_encode( $current[ $key ] ?? null ) ) {
				continue;
			}
			$changed++;
			if ( is_array( $value ) ) {
				WP_CLI::log( sprintf( '  %s: %d entries (now %d)', $key, count( $value ), count( (array) ( $current[ $key ] ?? array() ) ) ) );
				foreach ( array( 'gates', 'exit_lanes' ) as $table ) {
					if ( $table === $key ) {
						WP_CLI::log( '    ' . implode( ', ', wp_list_pluck( $value, 'name' ) ) );
					}
				}
			} else {
				WP_CLI::log( sprintf( '  %s: %s (now %s)', $key, wp_json_encode( $value ), wp_json_encode( $current[ $key ] ?? null ) ) );
			}
		}
		if ( 0 === $changed ) {
			WP_CLI::success( 'Nothing to change.' );
			return;
		}
		if ( ! empty( $assoc_args['dry-run'] ) ) {
			WP_CLI::success( "$changed settings would change. Nothing was written." );
			return;
		}
		WP_CLI::confirm( "Replace $changed settings on this server?", $assoc_args );

		// The settings page's own checks; they report through the settings errors and write vehicle_service.json
		require_once ABSPATH . 'wp-admin/includes/template.php';
		$sanitized = FSBHOA_Vehicle_Settings::get_instance()->sanitize_settings( $input );
		foreach ( get_settings_errors( FSBHOA_Vehicle_Settings::OPTION_NAME ) as $error ) {
			WP_CLI::warning( wp_strip_all_tags( $error['message'] ) );
		}
		update_option( FSBHOA_Vehicle_Settings::OPTION_NAME, $sanitized );
		WP_CLI::success( 'Imported. Enter the NVR password on the settings page if this server has none, and restart vehicle_service (sudo systemctl restart fsbhoa_vehicle).' );
	}
}

WP_CLI::add_command( 'fsbhoa-vehicle', 'FSBHOA_Vehicle_CLI' );
