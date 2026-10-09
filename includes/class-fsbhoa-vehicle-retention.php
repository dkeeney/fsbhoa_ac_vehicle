<?php
/**
 * Image retention for the vehicle access log.
 *
 * A daily WP-Cron job clears the plate and scene photos from ac_vehicle_log rows older
 * than the "Image Retention (days)" setting. The rows themselves (plate text, credential,
 * times) are kept. It runs at 03:05 local time; WP-Cron runs only when a request reaches
 * the site, and the server's crontab requests wp-cron.php at 03:11 (see ARCHITECTURE.md).
 *
 * @package FSBHOA_AC_Vehicle
 */

if ( ! defined( 'ABSPATH' ) ) {
	exit;
}

class FSBHOA_Vehicle_Retention {

	const CRON_HOOK  = 'fsbhoa_vehicle_purge_images';
	const RUN_TIME   = '03:05';
	const BATCH_SIZE = 500;

	public static function init() {
		add_action( 'init', array( __CLASS__, 'schedule' ) );
		add_action( self::CRON_HOOK, array( __CLASS__, 'purge_old_images' ) );
	}

	/**
	 * Schedule the daily purge if it isn't scheduled yet.
	 */
	public static function schedule() {
		if ( wp_next_scheduled( self::CRON_HOOK ) ) {
			return;
		}
		$next = new DateTime( 'today ' . self::RUN_TIME, wp_timezone() );
		if ( $next->getTimestamp() <= time() ) {
			$next->modify( '+1 day' );
		}
		wp_schedule_event( $next->getTimestamp(), 'daily', self::CRON_HOOK );
	}

	/**
	 * Remove the schedule (plugin deactivation).
	 */
	public static function unschedule() {
		wp_clear_scheduled_hook( self::CRON_HOOK );
	}

	/**
	 * Clear the photos from rows older than the retention period, in batches so a large
	 * backlog doesn't hold a long lock on the table. Returns the number of rows cleared.
	 */
	public static function purge_old_images() {
		global $wpdb;
		$table   = FSBHOA_Vehicle_DB::TABLE;
		$options = FSBHOA_Vehicle_Settings::get_options();
		$days    = absint( $options['image_retention_days'] );
		if ( $days < 1 ) {
			error_log( 'FSBHOA Vehicle: image retention is not set; skipping the photo purge.' );
			return 0;
		}

		// event_timestamp is local time (site time zone)
		$cutoff  = wp_date( 'Y-m-d H:i:s', time() - $days * DAY_IN_SECONDS );
		$cleared = 0;
		do {
			$batch = $wpdb->query( $wpdb->prepare(
				"UPDATE `{$table}`
				    SET context_image_data = NULL, lpr_image_data = NULL
				  WHERE event_timestamp < %s
				    AND ( context_image_data IS NOT NULL OR lpr_image_data IS NOT NULL )
				  LIMIT %d",
				$cutoff,
				self::BATCH_SIZE
			) );
			if ( false === $batch ) {
				error_log( 'FSBHOA Vehicle: photo purge failed: ' . $wpdb->last_error );
				break;
			}
			$cleared += $batch;
		} while ( $batch === self::BATCH_SIZE );

		if ( $cleared > 0 ) {
			error_log( sprintf( 'FSBHOA Vehicle: cleared photos from %d vehicle log rows older than %s (%d days).', $cleared, $cutoff, $days ) );
		}
		return $cleared;
	}
}
