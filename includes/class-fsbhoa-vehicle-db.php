<?php
/**
 * Database setup for the vehicle access log (ac_vehicle_log).
 *
 * This plugin owns ac_vehicle_log; core never references it. The table is created on
 * activation, and maybe_upgrade() brings an existing table up to date whenever
 * DB_VERSION changes (the plugin is usually already active when it is updated, so
 * activation alone isn't enough). The same schema is in db_schema.sql for reference.
 *
 * @package FSBHOA_AC_Vehicle
 */

if ( ! defined( 'ABSPATH' ) ) {
	exit;
}

class FSBHOA_Vehicle_DB {

	const TABLE          = 'ac_vehicle_log';
	const DB_VERSION     = '1';
	const VERSION_OPTION = 'fsbhoa_vehicle_db_version';

	/**
	 * Run install() when the stored schema version is out of date.
	 */
	public static function maybe_upgrade() {
		if ( get_option( self::VERSION_OPTION ) !== self::DB_VERSION ) {
			self::install();
		}
	}

	/**
	 * Create ac_vehicle_log, or add the columns and indexes an older table is missing.
	 * Records the schema version only when every step succeeded, so a failure is retried.
	 */
	public static function install() {
		global $wpdb;
		$table           = self::TABLE;
		$charset_collate = $wpdb->get_charset_collate();
		$ok              = true;

		$created = $wpdb->query( "CREATE TABLE IF NOT EXISTS `{$table}` (
			`vehicle_log_id` int NOT NULL AUTO_INCREMENT,
			`event_timestamp` datetime(3) NOT NULL COMMENT 'Local time (site time zone) of the first input',
			`gate_identifier` varchar(50) NOT NULL,
			`auth_id` varchar(50) DEFAULT NULL COMMENT 'Credential value or PIN presented at gate',
			`auth_type` varchar(32) DEFAULT NULL COMMENT 'Credential type for auth_id',
			`lpr_plate_string` varchar(20) DEFAULT NULL,
			`lpr_confidence` tinyint UNSIGNED DEFAULT NULL COMMENT 'Speco OCR confidence score 0-100',
			`is_circumvention` tinyint(1) NOT NULL DEFAULT '0' COMMENT '1 if loop traversed without auth',
			`context_image_data` mediumblob DEFAULT NULL,
			`lpr_image_data` mediumblob DEFAULT NULL,
			`raw_details` json DEFAULT NULL COMMENT 'Timing delta, Shelly loop state transitions, daemon telemetry',
			`created_at` datetime DEFAULT CURRENT_TIMESTAMP,
			PRIMARY KEY (`vehicle_log_id`),
			KEY `idx_timestamp_gate` (`event_timestamp`, `gate_identifier`),
			KEY `idx_lpr_plate` (`lpr_plate_string`),
			KEY `idx_auth_id` (`auth_id`),
			KEY `idx_circumvention` (`is_circumvention`, `event_timestamp`),
			KEY `idx_auth_lookup` (`auth_type`, `auth_id`)
		) ENGINE=InnoDB {$charset_collate};" );
		if ( false === $created ) {
			error_log( 'FSBHOA Vehicle: could not create ' . $table . ': ' . $wpdb->last_error );
			return false;
		}

		// Older tables (created before auth_type existed) are missing these.
		if ( ! $wpdb->get_var( "SHOW COLUMNS FROM `{$table}` LIKE 'auth_type'" ) ) {
			$ok = self::alter( "ADD COLUMN `auth_type` varchar(32) DEFAULT NULL COMMENT 'Credential type for auth_id' AFTER `auth_id`" ) && $ok;
		}
		if ( ! $wpdb->get_var( "SHOW INDEX FROM `{$table}` WHERE Key_name = 'idx_auth_lookup'" ) ) {
			$ok = self::alter( 'ADD KEY `idx_auth_lookup` (`auth_type`, `auth_id`)' ) && $ok;
		}

		if ( $ok ) {
			update_option( self::VERSION_OPTION, self::DB_VERSION );
		}
		return $ok;
	}

	private static function alter( $change ) {
		global $wpdb;
		if ( false === $wpdb->query( 'ALTER TABLE `' . self::TABLE . '` ' . $change ) ) {
			error_log( 'FSBHOA Vehicle: schema change failed (' . $change . '): ' . $wpdb->last_error );
			return false;
		}
		return true;
	}
}
