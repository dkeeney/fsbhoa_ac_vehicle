<?php
/**
 * Vehicle parts of core's live monitor, added through core's monitor hooks:
 * - fsbhoa_monitor_activity_columns: the "Vehicle Gate Traffic" column (left of the pedestrian log)
 * - fsbhoa_monitor_modals: the lightbox that shows a plate or scene photo
 *
 * @package FSBHOA_AC_Vehicle
 */

if ( ! defined( 'ABSPATH' ) ) {
	exit;
}

add_action( 'fsbhoa_monitor_activity_columns', 'fsbhoa_vehicle_render_monitor_column' );
add_action( 'fsbhoa_monitor_modals', 'fsbhoa_vehicle_render_monitor_modal' );

/**
 * Render the vehicle column and load its script. The script loads only where the column is drawn.
 */
function fsbhoa_vehicle_render_monitor_column() {
	wp_enqueue_script(
		'fsbhoa-vehicle-monitor-js',
		FSBHOA_AC_VEHICLE_URL . 'assets/js/fsbhoa-vehicle-monitor.js',
		array(), // No strict dependency handle
		FSBHOA_AC_VEHICLE_VERSION . '.' . time(),
		true
	);
	// The monitor routes require a logged-in admin; WordPress needs the nonce to recognize the user.
	wp_localize_script( 'fsbhoa-vehicle-monitor-js', 'fsbhoa_vehicle_vars', array(
		'nonce' => wp_create_nonce( 'wp_rest' ),
	) );
	?>
	<!-- LEFT: Vehicle Activity (fsbhoa_ac_vehicle) -->
	<div id="vehicle-log-column">
		<h2 class="text-xl font-semibold mb-4">Vehicle Gate Traffic</h2>
		<div class="bg-white rounded-xl shadow-md overflow-hidden">
			<div id="vehicle-log-container" style="height: 36rem; overflow-y: auto;">
				<ul id="vehicle-event-list" class="divide-y divide-gray-200">
					<li id="vehicle-log-placeholder" class="p-4 text-center text-gray-500">
						Waiting for vehicle events...
					</li>
				</ul>
			</div>
		</div>
	</div>
	<?php
}

/**
 * Render the lightbox for full-size vehicle photos (opened by openVehicleScene()).
 */
function fsbhoa_vehicle_render_monitor_modal() {
	?>
	<!-- Lightbox Modal for Full Scene Image (fsbhoa_ac_vehicle) -->
	<div id="fsbhoa-vehicle-modal" style="display:none; position: fixed; inset: 0; background: rgba(0,0,0,0.85); z-index: 99999; justify-content: center; align-items: center; padding: 20px;" onclick="this.style.display='none';">
		<div style="max-width: 90vw; max-height: 90vh; text-align: center;" onclick="event.stopPropagation();">
			<img id="fsbhoa-modal-img" src="" alt="Full Scene" style="max-width: 100%; max-height: 80vh; border-radius: 8px;">
			<div style="margin-top: 10px;">
				<button type="button" class="button" onclick="document.getElementById('fsbhoa-vehicle-modal').style.display='none';">Close</button>
			</div>
		</div>
	</div>
	<?php
}
