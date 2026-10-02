<?php
/**
 * Settings and Daemon Configuration for FSBHOA Vehicle Access Control.
 *
 * @package FSBHOA_AC_Vehicle
 */

if ( ! defined( 'ABSPATH' ) ) {
	exit;
}

class FSBHOA_Vehicle_Settings {

	const OPTION_GROUP = 'fsbhoa_vehicle_settings_group';
	const OPTION_NAME  = 'fsbhoa_vehicle_options';
	const PAGE_SLUG    = 'fsbhoa-vehicle-settings';

    private $vehicle_config_path = '/var/lib/fsbhoa/vehicle_service.json';
	private static $instance = null;

	public static function get_instance() {
		if ( null === self::$instance ) {
			self::$instance = new self();
		}
		return self::$instance;
	}

    private function __construct() {
		add_action( 'fsbhoa_register_admin_submenus', array( $this, 'register_admin_menu' ) );
		add_action( 'admin_init', array( $this, 'register_settings' ) );

        // Direct hook when vehicle settings are saved via options.php
        add_action( 'update_option_' . self::OPTION_NAME, array( $this, 'update_service_config' ) );
		add_action( 'add_option_' . self::OPTION_NAME, array( $this, 'update_service_config' ) );

		// Connection Hook: Listen to Core plugin master broadcast
		add_action( 'fsbhoa_update_service_configs', array( $this, 'update_service_config' ) );
	}

    public function register_admin_menu( $parent_slug ) {
		add_submenu_page(
			$parent_slug,
			__( 'Vehicle Service Settings', 'fsbhoa-ac-vehicle' ),
			__( 'Vehicle Service', 'fsbhoa-ac-vehicle' ),
			'manage_options',
			self::PAGE_SLUG,
			array( $this, 'render_settings_page' ),
			17
		);
	}

	public static function get_defaults() {
		return array(
			'daemon_host'          => '127.0.0.1',
			'daemon_port'          => 8088,
			'ingest_secret'        => '',
			'allowed_daemon_ips'   => '127.0.0.1, ::1',
			'correlation_window'   => 15,
			'image_retention_days' => 90,
			'enable_debug_logging' => 0,
		);
	}

	public static function get_options() {
		$options = get_option( self::OPTION_NAME, array() );
		return wp_parse_args( $options, self::get_defaults() );
	}

	public function register_settings() {
		register_setting(
			self::OPTION_GROUP,
			self::OPTION_NAME,
			array(
				'type'              => 'array',
				'sanitize_callback' => array( $this, 'sanitize_settings' ),
				'default'           => self::get_defaults(),
			)
		);

		add_settings_section(
			'fsbhoa_vehicle_ingest_section',
			__( 'Daemon Ingestion & REST Authentication', 'fsbhoa-ac-vehicle' ),
			array( $this, 'render_ingest_section_description' ),
			self::PAGE_SLUG
		);

		add_settings_field(
			'ingest_secret',
			__( 'Shared Ingest Secret', 'fsbhoa-ac-vehicle' ),
			array( $this, 'render_secret_field' ),
			self::PAGE_SLUG,
			'fsbhoa_vehicle_ingest_section',
			array( 'label_for' => 'fsbhoa_vehicle_ingest_secret' )
		);

		add_settings_field(
			'allowed_daemon_ips',
			__( 'Allowed Daemon IPs', 'fsbhoa-ac-vehicle' ),
			array( $this, 'render_allowed_ips_field' ),
			self::PAGE_SLUG,
			'fsbhoa_vehicle_ingest_section',
			array( 'label_for' => 'fsbhoa_vehicle_allowed_ips' )
		);

		add_settings_section(
			'fsbhoa_vehicle_daemon_section',
			__( 'vehicle_service Daemon Telemetry & Status', 'fsbhoa-ac-vehicle' ),
			array( $this, 'render_daemon_section_description' ),
			self::PAGE_SLUG
		);

		add_settings_field(
			'daemon_host',
			__( 'Daemon Host / IP', 'fsbhoa-ac-vehicle' ),
			array( $this, 'render_host_field' ),
			self::PAGE_SLUG,
			'fsbhoa_vehicle_daemon_section',
			array( 'label_for' => 'fsbhoa_vehicle_daemon_host' )
		);

		add_settings_field(
			'daemon_port',
			__( 'Daemon Port', 'fsbhoa-ac-vehicle' ),
			array( $this, 'render_port_field' ),
			self::PAGE_SLUG,
			'fsbhoa_vehicle_daemon_section',
			array( 'label_for' => 'fsbhoa_vehicle_daemon_port' )
		);

		add_settings_section(
			'fsbhoa_vehicle_policy_section',
			__( 'Correlation Window & Retention', 'fsbhoa-ac-vehicle' ),
			array( $this, 'render_policy_section_description' ),
			self::PAGE_SLUG
		);

		add_settings_field(
			'correlation_window',
			__( 'Correlation Window (seconds)', 'fsbhoa-ac-vehicle' ),
			array( $this, 'render_correlation_field' ),
			self::PAGE_SLUG,
			'fsbhoa_vehicle_policy_section',
			array( 'label_for' => 'fsbhoa_vehicle_correlation_window' )
		);

		add_settings_field(
			'image_retention_days',
			__( 'Image Retention (days)', 'fsbhoa-ac-vehicle' ),
			array( $this, 'render_retention_field' ),
			self::PAGE_SLUG,
			'fsbhoa_vehicle_policy_section',
			array( 'label_for' => 'fsbhoa_vehicle_retention_days' )
		);

		add_settings_field(
			'enable_debug_logging',
			__( 'Debug Logging', 'fsbhoa-ac-vehicle' ),
			array( $this, 'render_debug_field' ),
			self::PAGE_SLUG,
			'fsbhoa_vehicle_policy_section',
			array( 'label_for' => 'fsbhoa_vehicle_debug_logging' )
		);
	}

	public function sanitize_settings( $input ) {
		$sanitized = array();
		$defaults  = self::get_defaults();

		if ( ! empty( $input['daemon_host'] ) ) {
			$sanitized['daemon_host'] = sanitize_text_field( trim( $input['daemon_host'] ) );
		} else {
			$sanitized['daemon_host'] = $defaults['daemon_host'];
		}

		if ( isset( $input['daemon_port'] ) ) {
			$port = absint( $input['daemon_port'] );
			$sanitized['daemon_port'] = ( $port > 0 && $port <= 65535 ) ? $port : $defaults['daemon_port'];
		} else {
			$sanitized['daemon_port'] = $defaults['daemon_port'];
		}

		if ( isset( $input['ingest_secret'] ) ) {
			$sanitized['ingest_secret'] = sanitize_text_field( trim( $input['ingest_secret'] ) );
		} else {
			$sanitized['ingest_secret'] = '';
		}

		if ( isset( $input['allowed_daemon_ips'] ) ) {
			$ips = explode( ',', sanitize_text_field( $input['allowed_daemon_ips'] ) );
			$cleaned_ips = array();
			foreach ( $ips as $ip ) {
				$trimmed = trim( $ip );
				if ( ! empty( $trimmed ) ) {
					$cleaned_ips[] = $trimmed;
				}
			}
			$sanitized['allowed_daemon_ips'] = implode( ', ', $cleaned_ips );
		} else {
			$sanitized['allowed_daemon_ips'] = $defaults['allowed_daemon_ips'];
		}

		if ( isset( $input['correlation_window'] ) ) {
			$window = absint( $input['correlation_window'] );
			$sanitized['correlation_window'] = ( $window >= 3 && $window <= 120 ) ? $window : $defaults['correlation_window'];
		} else {
			$sanitized['correlation_window'] = $defaults['correlation_window'];
		}

		if ( isset( $input['image_retention_days'] ) ) {
			$days = absint( $input['image_retention_days'] );
			$sanitized['image_retention_days'] = ( $days >= 1 && $days <= 730 ) ? $days : $defaults['image_retention_days'];
		} else {
			$sanitized['image_retention_days'] = $defaults['image_retention_days'];
		}

		$sanitized['enable_debug_logging'] = ! empty( $input['enable_debug_logging'] ) ? 1 : 0;

        $this->write_config_from_array($sanitized );

		return $sanitized;
	}

	public function render_ingest_section_description() {
		echo '<p>' . esc_html__( 'Configure authentication tokens and IP restrictions for correlated vehicle events pushed by vehicle_service into the WordPress REST API.', 'fsbhoa-ac-vehicle' ) . '</p>';
	}

	public function render_daemon_section_description() {
		echo '<p>' . esc_html__( 'Connection settings for communicating with the local Go service daemon for health status, queue inspection, and live state.', 'fsbhoa-ac-vehicle' ) . '</p>';
	}

	public function render_policy_section_description() {
		echo '<p>' . esc_html__( 'State machine timing thresholds and local LPR plate image retention policies.', 'fsbhoa-ac-vehicle' ) . '</p>';
	}

	public function render_secret_field() {
		$options = self::get_options();
		?>
		<input type="password" 
				 id="fsbhoa_vehicle_ingest_secret" 
				 name="<?php echo esc_attr( self::OPTION_NAME . '[ingest_secret]' ); ?>" 
				 value="<?php echo esc_attr( $options['ingest_secret'] ); ?>" 
				 class="regular-text code" 
				 autocomplete="new-password" />
		<p class="description">
			<?php esc_html_e( 'Pre-shared bearer token configured in /var/lib/fsbhoa/vehicle_service.json sent in X-FSBHOA-Secret or Authorization header.', 'fsbhoa-ac-vehicle' ); ?>
		</p>
		<?php
	}

	public function render_allowed_ips_field() {
		$options = self::get_options();
		?>
		<input type="text" 
				 id="fsbhoa_vehicle_allowed_ips" 
				 name="<?php echo esc_attr( self::OPTION_NAME . '[allowed_daemon_ips]' ); ?>" 
				 value="<?php echo esc_attr( $options['allowed_daemon_ips'] ); ?>" 
				 class="regular-text code" />
		<p class="description">
			<?php esc_html_e( 'Comma-delimited IP addresses permitted to post raw event batches (e.g. 127.0.0.1, 10.0.10.5).', 'fsbhoa-ac-vehicle' ); ?>
		</p>
		<?php
	}

	public function render_host_field() {
		$options = self::get_options();
		?>
		<input type="text" 
				 id="fsbhoa_vehicle_daemon_host" 
				 name="<?php echo esc_attr( self::OPTION_NAME . '[daemon_host]' ); ?>" 
				 value="<?php echo esc_attr( $options['daemon_host'] ); ?>" 
				 class="regular-text code" />
		<p class="description">
			<?php esc_html_e( 'Hostname or internal IP running vehicle_service (default: 127.0.0.1).', 'fsbhoa-ac-vehicle' ); ?>
		</p>
		<?php
	}

	public function render_port_field() {
		$options = self::get_options();
		?>
		<input type="number" 
				 id="fsbhoa_vehicle_daemon_port" 
				 name="<?php echo esc_attr( self::OPTION_NAME . '[daemon_port]' ); ?>" 
				 value="<?php echo esc_attr( $options['daemon_port'] ); ?>" 
				 class="small-text code" 
				 min="1" 
				 max="65535" />
		<p class="description">
			<?php esc_html_e( 'HTTP control/telemetry port of the Go daemon (default: 8088).', 'fsbhoa-ac-vehicle' ); ?>
		</p>
		<?php
	}

	public function render_correlation_field() {
		$options = self::get_options();
		?>
		<input type="number" 
				 id="fsbhoa_vehicle_correlation_window" 
				 name="<?php echo esc_attr( self::OPTION_NAME . '[correlation_window]' ); ?>" 
				 value="<?php echo esc_attr( $options['correlation_window'] ); ?>" 
				 class="small-text" 
				 min="3" 
				 max="120" />
		<span><?php esc_html_e( 'seconds', 'fsbhoa-ac-vehicle' ); ?></span>
		<p class="description">
			<?php esc_html_e( 'Maximum sliding window elapsed between DoorKing gate activation, loop transition, and Speco LPR capture.', 'fsbhoa-ac-vehicle' ); ?>
		</p>
		<?php
	}

	public function render_retention_field() {
		$options = self::get_options();
		?>
		<input type="number" 
				 id="fsbhoa_vehicle_retention_days" 
				 name="<?php echo esc_attr( self::OPTION_NAME . '[image_retention_days]' ); ?>" 
				 value="<?php echo esc_attr( $options['image_retention_days'] ); ?>" 
				 class="small-text" 
				 min="1" 
				 max="730" />
		<span><?php esc_html_e( 'days', 'fsbhoa-ac-vehicle' ); ?></span>
		<p class="description">
			<?php esc_html_e( 'Retention schedule before purge cron cleans out binary plate/vehicle snapshot crops from storage.', 'fsbhoa-ac-vehicle' ); ?>
		</p>
		<?php
	}

	public function render_debug_field() {
		$options = self::get_options();
		?>
		<label for="fsbhoa_vehicle_debug_logging">
			<input type="checkbox" 
				 id="fsbhoa_vehicle_debug_logging" 
				 name="<?php echo esc_attr( self::OPTION_NAME . '[enable_debug_logging]' ); ?>" 
				 value="1" 
				 <?php checked( 1, $options['enable_debug_logging'] ); ?> />
			<?php esc_html_e( 'Enable verbose logging of correlation transitions and payload validation to debug.log', 'fsbhoa-ac-vehicle' ); ?>
		</label>
		<?php
	}

	public function render_settings_page() {
		if ( ! current_user_can( 'manage_options' ) ) {
			wp_die( esc_html__( 'You do not have sufficient permissions to access this page.', 'fsbhoa-ac-vehicle' ) );
		}

		$options     = self::get_options();
		$health_url  = sprintf( 'http://%s:%d/health', $options['daemon_host'], $options['daemon_port'] );
		$daemon_live = false;
		$status_msg  = __( 'Unknown', 'fsbhoa-ac-vehicle' );

		$response = wp_remote_get(
			$health_url,
			array(
				'timeout' => 2,
				'headers' => array( 'Accept' => 'application/json' ),
			)
		);

		if ( ! is_wp_error( $response ) && 200 === wp_remote_retrieve_response_code( $response ) ) {
			$daemon_live = true;
			$status_msg  = __( 'Active & Reachable', 'fsbhoa-ac-vehicle' );
		} else {
			$status_msg = __( 'Unreachable / Stopped', 'fsbhoa-ac-vehicle' );
		}
		?>
		<div class="wrap">
			<h1><?php echo esc_html( get_admin_page_title() ); ?></h1>
			<?php settings_errors(); ?>

			<div class="notice notice-<?php echo $daemon_live ? 'success' : 'warning'; ?> inline" style="margin: 15px 0;">
				<p>
					<strong><?php esc_html_e( 'vehicle_service Status:', 'fsbhoa-ac-vehicle' ); ?></strong>
					<span style="color: <?php echo $daemon_live ? '#46b450' : '#dc3232'; ?>; font-weight: 600;">
						<?php esc_html( $status_msg ); ?>
					</span>
					<code>(<?php echo esc_html( $health_url ); ?>)</code>
				</p>
			</div>

			<form action="options.php" method="post">
				<?php
				settings_fields( self::OPTION_GROUP );
				do_settings_sections( self::PAGE_SLUG );
				submit_button( __( 'Save Vehicle Settings', 'fsbhoa-ac-vehicle' ) );
				?>
			</form>
		</div>
		<?php
	}

    /**
	 * Writes the JSON config file directly from an array of options.
	 */
	public function write_config_from_array( $options ) {
		if ( empty( $this->vehicle_config_path ) ) {
			$this->vehicle_config_path = '/var/lib/fsbhoa/vehicle_service.json';
		}

		$wp_host = get_option( 'fsbhoa_ac_wp_host', 'access.fsbhoa.com' );

		$config = array(
			'daemon_host'          => sanitize_text_field( $options['daemon_host'] ?? '127.0.0.1' ),
			'daemon_port'          => absint( $options['daemon_port'] ?? 8088 ),
			'ingest_secret'        => sanitize_text_field( $options['ingest_secret'] ?? '' ),
			'allowed_daemon_ips'   => sanitize_text_field( $options['allowed_daemon_ips'] ?? '127.0.0.1, ::1' ),
			'correlation_window'   => absint( $options['correlation_window'] ?? 15 ),
			'image_retention_days' => absint( $options['image_retention_days'] ?? 90 ),
			'enable_debug_logging' => ! empty( $options['enable_debug_logging'] ) ? 1 : 0,
			'wordpress_host'       => sanitize_text_field( $wp_host ),
		);

		$json_data = json_encode($config, JSON_PRETTY_PRINT | JSON_UNESCAPED_SLASHES );

		$config_dir = dirname($this->vehicle_config_path );
		if ( ! is_dir( $config_dir ) ) {
			wp_mkdir_p( $config_dir );
		}
		@file_put_contents( $this->vehicle_config_path, $json_data );
	}

	/**
	 * Generates vehicle_service.json for the Go Service
	 */
	public function update_service_config() {
		$options = self::get_options();
		$this->write_config_from_array($options );
	}
}
