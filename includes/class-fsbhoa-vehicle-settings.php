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
			'ram_port'             => 8089,
			'correlation_window'   => 15,
			'enable_debug_logging' => 0,
			'doorking_events'      => 0,
			'ram_sources'          => '',
			'gates'                => array(),
			'exit_lanes'           => array(),
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
			__( 'REST Authentication', 'fsbhoa-ac-vehicle' ),
			array( $this, 'render_ingest_section_description' ),
			self::PAGE_SLUG
		);

		add_settings_field(
			'api_key_status',
			__( 'API Key', 'fsbhoa-ac-vehicle' ),
			array( $this, 'render_api_key_status_field' ),
			self::PAGE_SLUG,
			'fsbhoa_vehicle_ingest_section'
		);

		add_settings_section(
			'fsbhoa_vehicle_daemon_section',
			__( 'vehicle_service Ports', 'fsbhoa-ac-vehicle' ),
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
			__( 'Device Port (HTTP)', 'fsbhoa-ac-vehicle' ),
			array( $this, 'render_port_field' ),
			self::PAGE_SLUG,
			'fsbhoa_vehicle_daemon_section',
			array( 'label_for' => 'fsbhoa_vehicle_daemon_port' )
		);

		add_settings_field(
			'ram_port',
			__( 'DoorKing Stream Port (TCP)', 'fsbhoa-ac-vehicle' ),
			array( $this, 'render_ram_port_field' ),
			self::PAGE_SLUG,
			'fsbhoa_vehicle_daemon_section',
			array( 'label_for' => 'fsbhoa_vehicle_ram_port' )
		);

		add_settings_section(
			'fsbhoa_vehicle_sources_section',
			__( 'Gate Devices', 'fsbhoa-ac-vehicle' ),
			array( $this, 'render_sources_section_description' ),
			self::PAGE_SLUG
		);

		add_settings_field(
			'ram_sources',
			__( 'RAM Stream Sources', 'fsbhoa-ac-vehicle' ),
			array( $this, 'render_ip_list_field' ),
			self::PAGE_SLUG,
			'fsbhoa_vehicle_sources_section',
			array(
				'label_for'   => 'fsbhoa_vehicle_ram_sources',
				'key'         => 'ram_sources',
				'description' => __( 'Comma-delimited IP addresses of the PCs running DoorKing RAM whose Live Streaming output points at this server (the DoorKing stream port, above). Connections from any other address are refused. On the testbed, the workbench RAM arrives through the VPN as 192.168.70.3.', 'fsbhoa-ac-vehicle' ),
			)
		);

		add_settings_field(
			'gates',
			__( 'Entrance Lanes', 'fsbhoa-ac-vehicle' ),
			array( $this, 'render_lanes_field' ),
			self::PAGE_SLUG,
			'fsbhoa_vehicle_sources_section',
			array( 'table' => 'gates' )
		);

		add_settings_field(
			'exit_lanes',
			__( 'Exit Lanes', 'fsbhoa-ac-vehicle' ),
			array( $this, 'render_lanes_field' ),
			self::PAGE_SLUG,
			'fsbhoa_vehicle_sources_section',
			array( 'table' => 'exit_lanes' )
		);

		add_settings_section(
			'fsbhoa_vehicle_policy_section',
			__( 'Correlation & Logging', 'fsbhoa-ac-vehicle' ),
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
			'doorking_events',
			__( 'DoorKing-Only Events', 'fsbhoa-ac-vehicle' ),
			array( $this, 'render_doorking_events_field' ),
			self::PAGE_SLUG,
			'fsbhoa_vehicle_policy_section',
			array( 'label_for' => 'fsbhoa_vehicle_doorking_events' )
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

		$ram_port              = absint( $input['ram_port'] ?? 0 );
		$sanitized['ram_port'] = ( $ram_port > 0 && $ram_port <= 65535 ) ? $ram_port : $defaults['ram_port'];
		if ( $sanitized['ram_port'] === $sanitized['daemon_port'] ) {
			add_settings_error( self::OPTION_NAME, 'fsbhoa_vehicle_same_ports', __( 'The device port and the DoorKing stream port must differ; the stream port was set back to its default.', 'fsbhoa-ac-vehicle' ) );
			$sanitized['ram_port'] = 8088 === $sanitized['daemon_port'] ? 8089 : $defaults['ram_port'];
		}

		if ( isset( $input['correlation_window'] ) ) {
			$window = absint( $input['correlation_window'] );
			$sanitized['correlation_window'] = ( $window >= 3 && $window <= 120 ) ? $window : $defaults['correlation_window'];
		} else {
			$sanitized['correlation_window'] = $defaults['correlation_window'];
		}

		$sanitized['enable_debug_logging'] = ! empty( $input['enable_debug_logging'] ) ? 1 : 0;
		$sanitized['doorking_events']      = ! empty( $input['doorking_events'] ) ? 1 : 0;

		$sanitized['ram_sources'] = $this->sanitize_ip_list( $input['ram_sources'] ?? '', 'ram_sources' );
		$ips                      = array();
		$sanitized['gates']       = $this->sanitize_lanes( $input['gates'] ?? array(), 'gates', $ips );
		$sanitized['exit_lanes']  = $this->sanitize_lanes( $input['exit_lanes'] ?? array(), 'exit_lanes', $ips );

        if ( ! $this->write_config_from_array( $sanitized ) ) {
			add_settings_error(
				self::OPTION_NAME,
				'fsbhoa_vehicle_config_write_failed',
				sprintf(
					/* translators: %s: config file path */
					__( 'Settings saved, but %s could not be written. vehicle_service will keep its old settings. See debug.log.', 'fsbhoa-ac-vehicle' ),
					$this->vehicle_config_path
				)
			);
		}

		return $sanitized;
	}

	/**
	 * Keeps the valid IP addresses from a comma-delimited list, and warns about the rest.
	 */
	private function sanitize_ip_list( $value, $key ) {
		$valid = array();
		foreach ( explode( ',', sanitize_text_field( $value ) ) as $item ) {
			$item = trim( $item );
			if ( '' === $item ) {
				continue;
			}
			if ( filter_var( $item, FILTER_VALIDATE_IP ) ) {
				$valid[] = $item;
			} else {
				add_settings_error(
					self::OPTION_NAME,
					'fsbhoa_vehicle_bad_ip_' . $key,
					/* translators: %s: the rejected entry */
					sprintf( __( '"%s" is not an IP address and was not saved.', 'fsbhoa-ac-vehicle' ), $item )
				);
			}
		}
		return implode( ', ', array_unique( $valid ) );
	}

	/**
	 * The two lane tables. Entrance lanes (option and config key "gates") have a DoorKing
	 * controller (RAM account), a loop sensor and cameras; exit lanes only cameras. A car may
	 * enter at any entrance and leave at any exit, so the two aren't linked.
	 *
	 * @return array table key => array( 'account' => bool, 'devices' => field => label )
	 */
	private static function lane_tables() {
		return array(
			'gates'      => array(
				'title'   => __( 'Entrance lane', 'fsbhoa-ac-vehicle' ),
				'account' => true,
				'devices' => array(
					'loop_ip'     => __( 'Loop sensor', 'fsbhoa-ac-vehicle' ),
					'lpr_ip'      => __( 'LPR camera', 'fsbhoa-ac-vehicle' ),
					'overview_ip' => __( 'Overview camera', 'fsbhoa-ac-vehicle' ),
				),
			),
			'exit_lanes' => array(
				'title'   => __( 'Exit lane', 'fsbhoa-ac-vehicle' ),
				'account' => false,
				'devices' => array(
					'lpr_ip'      => __( 'LPR camera', 'fsbhoa-ac-vehicle' ),
					'overview_ip' => __( 'Overview camera', 'fsbhoa-ac-vehicle' ),
				),
			),
		);
	}

	/**
	 * Cleans one lane table. A row without a name is dropped (that is how a lane is removed).
	 * Names must be unique within the table, RAM accounts unique, every address a valid IP,
	 * and no address used twice in either table ($ips collects them across both); a row that
	 * breaks a rule is kept without the bad value, with a warning.
	 *
	 * @param array  $rows  The submitted rows.
	 * @param string $table 'gates' or 'exit_lanes'.
	 * @param array  $ips   Addresses already used: IP => where. Updated.
	 */
	private function sanitize_lanes( $rows, $table, &$ips ) {
		$spec     = self::lane_tables()[ $table ];
		$lanes    = array();
		$names    = array();
		$accounts = array();
		$warn     = function ( $code, $message ) use ( $table ) {
			add_settings_error( self::OPTION_NAME, 'fsbhoa_vehicle_' . $table . '_' . $code, $message );
		};

		foreach ( (array) $rows as $row ) {
			if ( ! is_array( $row ) ) {
				continue;
			}
			$name = substr( sanitize_text_field( $row['name'] ?? '' ), 0, 32 );
			if ( '' === $name ) {
				$others = array_filter( array_map( 'trim', array_intersect_key( $row, $spec['devices'] + array( 'ram_account' => '' ) ) ) );
				if ( $others ) {
					/* translators: %s: "Entrance lane" or "Exit lane" */
					$warn( 'noname', sprintf( __( '%s row without a name was removed. Every lane needs a name.', 'fsbhoa-ac-vehicle' ), $spec['title'] ) );
				}
				continue;
			}
			if ( isset( $names[ strtolower( $name ) ] ) ) {
				/* translators: 1: "Entrance lane" or "Exit lane", 2: lane name */
				$warn( 'dupname', sprintf( __( '%1$s "%2$s" appeared twice; the second was removed.', 'fsbhoa-ac-vehicle' ), $spec['title'], $name ) );
				continue;
			}
			$names[ strtolower( $name ) ] = true;
			$where                        = $spec['title'] . ' ' . $name;
			$lane                         = array( 'name' => $name );

			if ( $spec['account'] ) {
				$account             = sanitize_text_field( $row['ram_account'] ?? '' );
				$lane['ram_account'] = '';
				if ( '' !== $account ) {
					if ( isset( $accounts[ strtoupper( $account ) ] ) ) {
						/* translators: 1: RAM account, 2: lane, 3: the other lane's name */
						$warn( 'dupaccount', sprintf( __( 'RAM account "%1$s" was removed from %2$s: it already belongs to %3$s.', 'fsbhoa-ac-vehicle' ), $account, $where, $accounts[ strtoupper( $account ) ] ) );
					} else {
						$accounts[ strtoupper( $account ) ] = $name;
						$lane['ram_account']                = $account;
					}
				}
			}

			foreach ( $spec['devices'] as $key => $label ) {
				$ip           = trim( sanitize_text_field( $row[ $key ] ?? '' ) );
				$lane[ $key ] = '';
				if ( '' === $ip ) {
					continue;
				}
				if ( ! filter_var( $ip, FILTER_VALIDATE_IP ) ) {
					/* translators: 1: the rejected entry, 2: device, 3: lane */
					$warn( 'badip', sprintf( __( '"%1$s" (%2$s, %3$s) is not an IP address and was not saved.', 'fsbhoa-ac-vehicle' ), $ip, $label, $where ) );
				} elseif ( isset( $ips[ $ip ] ) ) {
					/* translators: 1: IP address, 2: device, 3: lane, 4: where it is already used */
					$warn( 'dupip', sprintf( __( '%1$s was removed from %2$s, %3$s: it is already the %4$s.', 'fsbhoa-ac-vehicle' ), $ip, $label, $where, $ips[ $ip ] ) );
				} else {
					$ips[ $ip ]   = $label . ', ' . $where;
					$lane[ $key ] = $ip;
				}
			}
			$lanes[] = $lane;
		}
		return $lanes;
	}

	/**
	 * One lane table: a row per lane, plus a blank row for adding one.
	 *
	 * @param array $args 'table' => 'gates' or 'exit_lanes'.
	 */
	public function render_lanes_field( $args ) {
		$table   = $args['table'];
		$spec    = self::lane_tables()[ $table ];
		$options = self::get_options();
		$lanes   = is_array( $options[ $table ] ) ? array_values( $options[ $table ] ) : array();
		$lanes[] = array(); // the blank row
		$field   = function ( $i, $key ) use ( $table ) {
			return self::OPTION_NAME . '[' . $table . '][' . $i . '][' . $key . ']';
		};
		?>
		<table class="widefat striped" style="max-width: 900px;">
			<thead>
				<tr>
					<th><?php esc_html_e( 'Name', 'fsbhoa-ac-vehicle' ); ?></th>
					<?php if ( $spec['account'] ) : ?>
						<th><?php esc_html_e( 'RAM account', 'fsbhoa-ac-vehicle' ); ?></th>
					<?php endif; ?>
					<?php foreach ( $spec['devices'] as $label ) : ?>
						<th><?php echo esc_html( $label ); ?></th>
					<?php endforeach; ?>
				</tr>
			</thead>
			<tbody>
				<?php foreach ( $lanes as $i => $lane ) : ?>
					<tr>
						<td><input type="text" name="<?php echo esc_attr( $field( $i, 'name' ) ); ?>" value="<?php echo esc_attr( $lane['name'] ?? '' ); ?>" maxlength="32" size="10" placeholder="<?php echo empty( $lane ) ? esc_attr__( 'New lane', 'fsbhoa-ac-vehicle' ) : ''; ?>" /></td>
						<?php if ( $spec['account'] ) : ?>
							<td><input type="text" name="<?php echo esc_attr( $field( $i, 'ram_account' ) ); ?>" value="<?php echo esc_attr( $lane['ram_account'] ?? '' ); ?>" size="16" /></td>
						<?php endif; ?>
						<?php foreach ( array_keys( $spec['devices'] ) as $key ) : ?>
							<td><input type="text" class="code" name="<?php echo esc_attr( $field( $i, $key ) ); ?>" value="<?php echo esc_attr( $lane[ $key ] ?? '' ); ?>" size="14" /></td>
						<?php endforeach; ?>
					</tr>
				<?php endforeach; ?>
			</tbody>
		</table>
		<p class="description">
			<?php
			if ( $spec['account'] ) {
				esc_html_e( 'One row per entrance lane; each has its own DoorKing controller, loop sensor and cameras. The name is the gate shown in the vehicle log and on the monitor. The RAM account is the account name exactly as RAM sends it (as in RAM\'s account list); it is how a DoorKing record is matched to its lane. The addresses are the devices\' IP addresses: only they may call vehicle_service\'s webhooks, and the address tells which lane a call is from.', 'fsbhoa-ac-vehicle' );
			} else {
				esc_html_e( 'One row per exit lane. Exit lanes have no DoorKing controller or loop sensor; every plate read on one is logged as a vehicle leaving, under the lane\'s name. A car may enter at any entrance and leave at any exit, so exits aren\'t tied to an entrance. The name may repeat an entrance\'s name (e.g. South). The overview camera is optional.', 'fsbhoa-ac-vehicle' );
			}
			echo ' ';
			esc_html_e( 'Fill in the blank row to add a lane; clear a lane\'s name to remove it. A device address may be used only once, in either table.', 'fsbhoa-ac-vehicle' );
			?>
		</p>
		<?php
	}

	/**
	 * Splits a saved comma-delimited list into an array for vehicle_service.json.
	 */
	private static function ip_list_array( $value ) {
		return array_values( array_filter( array_map( 'trim', explode( ',', (string) $value ) ) ) );
	}

	public function render_sources_section_description() {
		echo '<p>' . esc_html__( 'The entrance and exit lanes, and the devices allowed to send inputs to vehicle_service. Every input is recorded in the raw-input journal (/var/lib/fsbhoa/vehicle_journal), tagged with its lane. This server itself is always allowed. Changes apply as soon as the settings are saved, without restarting vehicle_service.', 'fsbhoa-ac-vehicle' ) . '</p>';
	}

	public function render_ip_list_field( $args ) {
		$options = self::get_options();
		?>
		<input type="text"
				 id="<?php echo esc_attr( $args['label_for'] ); ?>"
				 name="<?php echo esc_attr( self::OPTION_NAME . '[' . $args['key'] . ']' ); ?>"
				 value="<?php echo esc_attr( $options[ $args['key'] ] ); ?>"
				 class="regular-text code" />
		<p class="description"><?php echo esc_html( $args['description'] ); ?></p>
		<?php
	}

	public function render_ingest_section_description() {
		echo '<p>' . esc_html__( 'vehicle_service posts each finished vehicle event to core\'s vehicle log (POST /vehicle-event), which checks the API key. Photo retention is a Live Monitor setting in core.', 'fsbhoa-ac-vehicle' ) . '</p>';
	}

	public function render_daemon_section_description() {
		echo '<p>' . esc_html__( 'vehicle_service runs on this machine and listens on two ports, on all interfaces. Changing a port takes effect when vehicle_service is restarted (sudo systemctl restart fsbhoa_vehicle); the devices pointing at the old port must be changed too.', 'fsbhoa-ac-vehicle' ) . '</p>';
	}

	public function render_policy_section_description() {
		echo '<p>' . esc_html__( 'State machine timing and what vehicle_service logs.', 'fsbhoa-ac-vehicle' ) . '</p>';
	}

	public function render_api_key_status_field() {
		$has_key = '' !== get_option( 'fsbhoa_ac_verify_api_key', '' );
		?>
		<span style="color: <?php echo $has_key ? '#46b450' : '#dc3232'; ?>; font-weight: 600;">
			<?php echo $has_key ? esc_html__( 'Set', 'fsbhoa-ac-vehicle' ) : esc_html__( 'Not set', 'fsbhoa-ac-vehicle' ); ?>
		</span>
		<p class="description">
			<?php esc_html_e( 'vehicle_service sends the Access Verification API Key (FSBHOA AC General settings) as X-API-KEY. It is copied into vehicle_service.json when these settings or the core settings are saved; restart vehicle_service afterwards. Without a key, every vehicle event is refused.', 'fsbhoa-ac-vehicle' ); ?>
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
			<?php esc_html_e( 'Address this page checks vehicle_service\'s health at (default: 127.0.0.1). vehicle_service runs on this machine and listens on all interfaces; its health check answers only this machine.', 'fsbhoa-ac-vehicle' ); ?>
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
			<?php esc_html_e( 'HTTP port (default 8088). The gate devices call in here: each loop sensor Shelly\'s webhook (/webhook/loop?state=on or off), and cameras if they push. Only the lane tables\' device addresses may call. This page reads vehicle_service\'s health here, and the RAM PC\'s DK_Sync.ahk reads /ram-status here.', 'fsbhoa-ac-vehicle' ); ?>
		</p>
		<?php
	}

	public function render_ram_port_field() {
		$options = self::get_options();
		?>
		<input type="number"
				 id="fsbhoa_vehicle_ram_port"
				 name="<?php echo esc_attr( self::OPTION_NAME . '[ram_port]' ); ?>"
				 value="<?php echo esc_attr( $options['ram_port'] ); ?>"
				 class="small-text code"
				 min="1"
				 max="65535" />
		<p class="description">
			<?php esc_html_e( 'TCP port (default 8089) that DoorKing RAM\'s Live Streaming connects to. Each RAM account\'s Live Streaming target (IP Address, Raw Text format) must be this server\'s address and this port. Only the RAM Stream Sources addresses may connect.', 'fsbhoa-ac-vehicle' ); ?>
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

	public function render_doorking_events_field() {
		$options = self::get_options();
		?>
		<label for="fsbhoa_vehicle_doorking_events">
			<input type="checkbox"
				 id="fsbhoa_vehicle_doorking_events"
				 name="<?php echo esc_attr( self::OPTION_NAME . '[doorking_events]' ); ?>"
				 value="1"
				 <?php checked( 1, $options['doorking_events'] ); ?> />
			<?php esc_html_e( 'Log each DoorKing authentication (windshield tag or gate code) as a vehicle event on its own, without a loop or plate, so it shows in the vehicle log and on the monitor at once. Repeat reads of a tag sitting at the reader count once. A stopgap until loop and plate correlation is built; leave it off in production. Applied without a restart.', 'fsbhoa-ac-vehicle' ); ?>
		</label>
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
			<?php esc_html_e( 'Log every loop, DoorKing and camera input in the vehicle_service journal (journalctl -u fsbhoa_vehicle). Dispatched events, warnings and errors are always logged. Restart vehicle_service after changing this.', 'fsbhoa-ac-vehicle' ); ?>
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

		$health = null;
		if ( ! is_wp_error( $response ) && 200 === wp_remote_retrieve_response_code( $response ) ) {
			$daemon_live = true;
			$status_msg  = __( 'Active & Reachable', 'fsbhoa-ac-vehicle' );
			$health      = json_decode( wp_remote_retrieve_body( $response ), true );
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
						<?php echo esc_html( $status_msg ); ?>
					</span>
					<code>(<?php echo esc_html( $health_url ); ?>)</code>
				</p>
			</div>

			<?php
			if ( is_array( $health ) ) {
				$this->render_input_status( $health );
			}
			?>

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
	 * Shows whether DoorKing RAM is connected, and when each kind of input last arrived.
	 *
	 * RAM doesn't reconnect by itself after its connection drops (a reboot, a stopped
	 * vehicle_service, a network break; systemd holds it across a restart of the service,
	 * see CLAUDE.md "RAM connection hold"); the RAM PC's DK_Sync.ahk restarts RAM,
	 * which connects when it starts. Until then no live transactions arrive, so a missing
	 * connection is an error.
	 *
	 * @param array $health The decoded /health response.
	 */
	private function render_input_status( $health ) {
		$streams   = isset( $health['ram_streams'] ) && is_array( $health['ram_streams'] ) ? $health['ram_streams'] : array();
		$last_seen = isset( $health['last_seen'] ) && is_array( $health['last_seen'] ) ? $health['last_seen'] : array();
		$uptime    = isset( $health['uptime'] ) ? absint( $health['uptime'] ) : 0;
		$options   = self::get_options();
		$expected  = '' !== trim( (string) $options['ram_sources'] );

		$format = function ( $iso ) {
			$ts = strtotime( (string) $iso );
			if ( ! $ts ) {
				return '';
			}
			/* translators: 1: date and time, 2: how long ago */
			return sprintf( __( '%1$s (%2$s ago)', 'fsbhoa-ac-vehicle' ), wp_date( 'M j, g:i:s a', $ts ), human_time_diff( $ts ) );
		};
		?>
		<div class="notice notice-<?php echo ( $streams || ! $expected ) ? 'info' : 'error'; ?> inline" style="margin: 15px 0;">
			<p><strong><?php esc_html_e( 'DoorKing RAM Live Streaming', 'fsbhoa-ac-vehicle' ); ?></strong></p>
			<?php if ( $streams ) : ?>
				<ul style="list-style: disc; margin-left: 20px;">
					<?php foreach ( $streams as $stream ) : ?>
						<li>
							<?php
							$accounts = ! empty( $stream['accounts'] ) ? implode( ', ', array_map( 'strval', (array) $stream['accounts'] ) ) : __( 'no records yet', 'fsbhoa-ac-vehicle' );
							printf(
								/* translators: 1: IP address, 2: RAM account names, 3: connection time, 4: last record time */
								esc_html__( 'Connected from %1$s (accounts: %2$s) since %3$s; last record %4$s.', 'fsbhoa-ac-vehicle' ),
								'<code>' . esc_html( $stream['remote'] ?? '' ) . '</code>',
								esc_html( $accounts ),
								esc_html( $format( $stream['connected_at'] ?? '' ) ),
								esc_html( ! empty( $stream['last_record_at'] ) ? $format( $stream['last_record_at'] ) : __( 'none', 'fsbhoa-ac-vehicle' ) )
							);
							?>
						</li>
					<?php endforeach; ?>
				</ul>
			<?php elseif ( $expected ) : ?>
				<p>
					<?php esc_html_e( "No RAM connection is open, so live DoorKing transactions are not being received. RAM does not reconnect by itself, but it connects when it starts: the RAM PC's DK_Sync.ahk script restarts RAM within seconds when its StreamStatusUrl setting points here. If this lasts, check that the script and RAM are running. Transactions in between are still in RAM's own history.", 'fsbhoa-ac-vehicle' ); ?>
				</p>
			<?php else : ?>
				<p><?php esc_html_e( 'No RAM stream sources are configured (Gate Devices, below).', 'fsbhoa-ac-vehicle' ); ?></p>
			<?php endif; ?>

			<p>
				<?php
				$labels = array(
					'ram_record'      => __( 'DoorKing record', 'fsbhoa-ac-vehicle' ),
					'loop'            => __( 'Loop change', 'fsbhoa-ac-vehicle' ),
					'plate'           => __( 'Plate read', 'fsbhoa-ac-vehicle' ),
					'ram_refused'     => __( 'Refused RAM connection', 'fsbhoa-ac-vehicle' ),
					'webhook_refused' => __( 'Refused device call', 'fsbhoa-ac-vehicle' ),
				);
				$lines  = array();
				foreach ( $labels as $kind => $label ) {
					$when    = ! empty( $last_seen[ $kind ] ) ? $format( $last_seen[ $kind ] ) : __( 'none', 'fsbhoa-ac-vehicle' );
					$lines[] = esc_html( $label . ': ' . $when );
				}
				echo implode( '<br>', $lines ); // phpcs:ignore WordPress.Security.EscapeOutput.OutputNotEscaped -- each line escaped above
				?>
				<br><em>
				<?php
				/* translators: %s: how long vehicle_service has been running */
				echo esc_html( sprintf( __( 'Times are since vehicle_service started, %s ago. Every input is in the journal, /var/lib/fsbhoa/vehicle_journal.', 'fsbhoa-ac-vehicle' ), human_time_diff( time() - $uptime ) ) );
				?>
				</em>
			</p>
		</div>
		<?php
	}

    /**
	 * Writes the JSON config file directly from an array of options.
	 *
	 * The file holds the API key, so it is written 0640 (owner www-data, group www-data;
	 * the service runs as group www-data). Written to a temp file and renamed, so the
	 * service never reads a half-written file. Returns false on failure.
	 */
	public function write_config_from_array( $options ) {
		if ( empty( $this->vehicle_config_path ) ) {
			$this->vehicle_config_path = '/var/lib/fsbhoa/vehicle_service.json';
		}

		// vehicle_service posts to WordPress on this machine (127.0.0.1); this site's own host
		// name goes in the Host header. Never another server: the testbed must not post to production.
		$wp_host     = wp_parse_url( home_url(), PHP_URL_HOST );
		$environment = defined( 'FSBHOA_AC_ENVIRONMENT' ) ? FSBHOA_AC_ENVIRONMENT : '';

		$config = array(
			'daemon_host'          => sanitize_text_field( $options['daemon_host'] ?? '127.0.0.1' ),
			'daemon_port'          => absint( $options['daemon_port'] ?? 8088 ),
			'ram_port'             => absint( $options['ram_port'] ?? 8089 ),
			'api_key'              => get_option( 'fsbhoa_ac_verify_api_key', '' ),
			'correlation_window'   => absint( $options['correlation_window'] ?? 15 ),
			'enable_debug_logging' => ! empty( $options['enable_debug_logging'] ) ? 1 : 0,
			'doorking_events'      => ! empty( $options['doorking_events'] ),
			'wordpress_host'       => sanitize_text_field( $wp_host ? $wp_host : '127.0.0.1' ),
			'environment'          => sanitize_text_field( $environment ),
			'ram_sources'          => self::ip_list_array( $options['ram_sources'] ?? '' ),
			'gates'                => array_values( is_array( $options['gates'] ?? null ) ? $options['gates'] : array() ),
			'exit_lanes'           => array_values( is_array( $options['exit_lanes'] ?? null ) ? $options['exit_lanes'] : array() ),
		);

		$json_data = json_encode($config, JSON_PRETTY_PRINT | JSON_UNESCAPED_SLASHES );

		$config_dir = dirname($this->vehicle_config_path );
		if ( ! is_dir( $config_dir ) ) {
			wp_mkdir_p( $config_dir );
		}

		$tmp_path = $this->vehicle_config_path . '.tmp';
		if ( false === $json_data
			|| false === file_put_contents( $tmp_path, $json_data )
			|| ! chmod( $tmp_path, 0640 )
			|| ! rename( $tmp_path, $this->vehicle_config_path ) ) {
			$error = error_get_last();
			error_log( 'FSBHOA Vehicle: failed to write ' . $this->vehicle_config_path . ': ' . ( $error['message'] ?? 'unknown error' ) );
			if ( file_exists( $tmp_path ) ) {
				unlink( $tmp_path );
			}
			return false;
		}
		return true;
	}

	/**
	 * Generates vehicle_service.json for the Go Service
	 */
	public function update_service_config() {
		$options = self::get_options();
		$this->write_config_from_array($options );
	}
}
