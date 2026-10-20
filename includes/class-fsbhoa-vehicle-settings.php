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

	// The NAS folder (CIFS mount) holding the DoorKing automation folders; RAM's Disk File goes in
	// its Live_Testbed or Live_Production folder.
	const RAM_FILE_BASE_DIR = '/mnt/shared/Automation/DoorKing';

	// The RAM PC's heartbeat, written by DoorKing's DK_Sync.ahk in the RAM Stream File's folder
	// every minute (a contract with that script), and how old it may get before the page warns.
	const RAM_PC_STATUS_FILE  = 'ram_pc_status.txt';
	const RAM_PC_STATUS_STALE = 300;

	// Where the LPR cameras' FTP uploads land (lpr_ftp/setup.sh makes it the upload account's home).
	const LPR_DROP_DIR_DEFAULT = '/var/lib/fsbhoa/lpr_drop';

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
			'correlation_window'   => 15,
			'enable_debug_logging' => 0,
			'doorking_events'      => 0,
			'plate_events'         => 0,
			'ram_file'             => '',
			'lpr_drop_dir'         => self::LPR_DROP_DIR_DEFAULT,
			'gates'                => array(),
			'exit_lanes'           => array(),
			'motion_regions'       => array(),
			'count_zones'          => array(),
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
			__( 'vehicle_service Port', 'fsbhoa-ac-vehicle' ),
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

		add_settings_section(
			'fsbhoa_vehicle_sources_section',
			__( 'Gate Devices', 'fsbhoa-ac-vehicle' ),
			array( $this, 'render_sources_section_description' ),
			self::PAGE_SLUG
		);

		add_settings_field(
			'ram_file',
			__( 'RAM Stream File', 'fsbhoa-ac-vehicle' ),
			array( $this, 'render_ram_file_field' ),
			self::PAGE_SLUG,
			'fsbhoa_vehicle_sources_section',
			array( 'label_for' => 'fsbhoa_vehicle_ram_file' )
		);

		add_settings_field(
			'lpr_drop_dir',
			__( 'LPR Upload Folder', 'fsbhoa-ac-vehicle' ),
			array( $this, 'render_lpr_drop_dir_field' ),
			self::PAGE_SLUG,
			'fsbhoa_vehicle_sources_section',
			array( 'label_for' => 'fsbhoa_vehicle_lpr_drop_dir' )
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

		add_settings_field(
			'motion_regions',
			__( 'Motion Regions', 'fsbhoa-ac-vehicle' ),
			array( $this, 'render_motion_regions_field' ),
			self::PAGE_SLUG,
			'fsbhoa_vehicle_sources_section',
			array( 'label_for' => 'fsbhoa_vehicle_motion_regions' )
		);

		add_settings_field(
			'count_zones',
			__( 'Car Count Zones', 'fsbhoa-ac-vehicle' ),
			array( $this, 'render_count_zones_field' ),
			self::PAGE_SLUG,
			'fsbhoa_vehicle_sources_section',
			array( 'label_for' => 'fsbhoa_vehicle_count_zones' )
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
			'plate_events',
			__( 'Plate Read Events', 'fsbhoa-ac-vehicle' ),
			array( $this, 'render_plate_events_field' ),
			self::PAGE_SLUG,
			'fsbhoa_vehicle_policy_section',
			array( 'label_for' => 'fsbhoa_vehicle_plate_events' )
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

		if ( isset( $input['correlation_window'] ) ) {
			$window = absint( $input['correlation_window'] );
			$sanitized['correlation_window'] = ( $window >= 3 && $window <= 120 ) ? $window : $defaults['correlation_window'];
		} else {
			$sanitized['correlation_window'] = $defaults['correlation_window'];
		}

		$sanitized['enable_debug_logging'] = ! empty( $input['enable_debug_logging'] ) ? 1 : 0;
		$sanitized['doorking_events']      = ! empty( $input['doorking_events'] ) ? 1 : 0;
		$sanitized['plate_events']         = ! empty( $input['plate_events'] ) ? 1 : 0;

		$sanitized['ram_file']    = $this->sanitize_ram_file( $input['ram_file'] ?? '' );
		$sanitized['lpr_drop_dir'] = $this->sanitize_lpr_drop_dir( $input['lpr_drop_dir'] ?? '' );
		$ips                      = array();
		$sanitized['gates']       = $this->sanitize_lanes( $input['gates'] ?? array(), 'gates', $ips );
		$sanitized['exit_lanes']  = $this->sanitize_lanes( $input['exit_lanes'] ?? array(), 'exit_lanes', $ips );
		$sanitized['motion_regions'] = $this->sanitize_motion_regions( $input['motion_regions'] ?? '', $sanitized );
		$sanitized['count_zones']    = $this->sanitize_count_zones( $input['count_zones'] ?? '', $sanitized );

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
	 * This server's environment (FSBHOA_AC_ENVIRONMENT), or '' if it is missing or unknown.
	 */
	private static function environment() {
		$env = defined( 'FSBHOA_AC_ENVIRONMENT' ) ? strtolower( trim( (string) FSBHOA_AC_ENVIRONMENT ) ) : '';
		return in_array( $env, array( 'testbed', 'production' ), true ) ? $env : '';
	}

	/**
	 * The suggested RAM Stream File path for this server: Live_Testbed or Live_Production
	 * beside the DoorKing sync folders on the NAS.
	 */
	public static function default_ram_file() {
		$env = self::environment();
		return self::RAM_FILE_BASE_DIR . '/Live_' . ( $env ? ucfirst( $env ) : 'Unconfigured' ) . '/stream.txt';
	}

	/**
	 * Keeps the RAM Stream File path if it is absolute and its folder names this server's
	 * environment, so a server never reads the other environment's transactions. vehicle_service
	 * checks the same. Empty turns the file off.
	 */
	private function sanitize_ram_file( $value ) {
		$path = trim( sanitize_text_field( (string) $value ) );
		if ( '' === $path ) {
			return '';
		}
		$env    = self::environment();
		$folder = strtolower( basename( dirname( $path ) ) );
		if ( '/' !== substr( $path, 0, 1 ) || '' === $env || false === strpos( $folder, $env ) ) {
			add_settings_error(
				self::OPTION_NAME,
				'fsbhoa_vehicle_ram_file',
				sprintf(
					/* translators: 1: the path entered, 2: the environment */
					__( 'RAM Stream File %1$s was not saved: it must be a full path whose folder name contains this server\'s environment (%2$s), such as Live_Testbed. No DoorKing records will be read until it is set.', 'fsbhoa-ac-vehicle' ),
					$path,
					$env ? $env : __( 'not set in wp-config.php', 'fsbhoa-ac-vehicle' )
				)
			);
			return '';
		}
		return $path;
	}

	/**
	 * Keeps the LPR Upload Folder if it is a full path; empty means the default.
	 */
	private function sanitize_lpr_drop_dir( $value ) {
		$path = untrailingslashit( trim( sanitize_text_field( (string) $value ) ) );
		if ( '' === $path ) {
			return self::LPR_DROP_DIR_DEFAULT;
		}
		if ( '/' !== substr( $path, 0, 1 ) || false !== strpos( $path, '..' ) ) {
			add_settings_error(
				self::OPTION_NAME,
				'fsbhoa_vehicle_lpr_drop_dir',
				sprintf(
					/* translators: 1: the path entered, 2: the default path */
					__( 'LPR Upload Folder %1$s was not saved: it must be a full path. Using %2$s.', 'fsbhoa-ac-vehicle' ),
					$path,
					self::LPR_DROP_DIR_DEFAULT
				)
			);
			return self::LPR_DROP_DIR_DEFAULT;
		}
		return $path;
	}

	/**
	 * The cameras motion regions may be set on: every lane's overview and keypad camera.
	 *
	 * @return string[] camera IP addresses
	 */
	private static function motion_cameras( $options ) {
		$cameras = array();
		foreach ( array( 'gates', 'exit_lanes' ) as $table ) {
			foreach ( (array) ( $options[ $table ] ?? array() ) as $lane ) {
				foreach ( array( 'overview_ip', 'keypad_ip' ) as $key ) {
					if ( ! empty( $lane[ $key ] ) ) {
						$cameras[] = $lane[ $key ];
					}
				}
			}
		}
		return $cameras;
	}

	/**
	 * Parses the Motion Regions text, one region a line:
	 * "<camera IP> <name> <left> <top> <right> <bottom>", the edges in percent of the picture.
	 * A line that doesn't fit is dropped with a message.
	 *
	 * @return array[] camera, name, left, top, right, bottom
	 */
	private function sanitize_motion_regions( $text, $options ) {
		if ( is_array( $text ) ) { // already parsed (an option saved before)
			$text = self::motion_regions_text( $text );
		}
		$cameras = self::motion_cameras( $options );
		$regions = array();
		$seen    = array();
		foreach ( preg_split( '/\r\n|\r|\n/', (string) $text ) as $line ) {
			$line = trim( sanitize_text_field( $line ) );
			if ( '' === $line || '#' === $line[0] ) {
				continue;
			}
			$parts = preg_split( '/\s+/', $line );
			$error = '';
			$kind  = '';
			if ( 7 === count( $parts ) && 'bright' === $parts[6] ) {
				$kind = array_pop( $parts );
			}
			if ( 6 !== count( $parts ) ) {
				$error = __( 'it needs a camera address, a name and four edges, and may end with "bright"', 'fsbhoa-ac-vehicle' );
			} elseif ( ! in_array( $parts[0], $cameras, true ) ) {
				$error = __( 'the camera is not a lane\'s overview or keypad camera', 'fsbhoa-ac-vehicle' );
			} elseif ( ! preg_match( '/^[A-Za-z0-9_-]{1,20}$/', $parts[1] ) ) {
				$error = __( 'the name may have only letters, digits, - and _', 'fsbhoa-ac-vehicle' );
			} elseif ( isset( $seen[ $parts[0] . ' ' . $parts[1] ] ) ) {
				$error = __( 'the camera already has a region of that name', 'fsbhoa-ac-vehicle' );
			} else {
				$edges = array_map( 'floatval', array_slice( $parts, 2 ) );
				$error = '';
				foreach ( array_slice( $parts, 2 ) as $edge ) {
					if ( ! is_numeric( $edge ) || $edge < 0 || $edge > 100 ) {
						$error = __( 'the edges must be numbers from 0 to 100', 'fsbhoa-ac-vehicle' );
					}
				}
				if ( '' === $error && ( $edges[0] >= $edges[2] || $edges[1] >= $edges[3] ) ) {
					$error = __( 'left must be less than right, and top less than bottom', 'fsbhoa-ac-vehicle' );
				}
			}
			if ( '' !== $error ) {
				add_settings_error(
					self::OPTION_NAME,
					'fsbhoa_vehicle_motion_region',
					sprintf(
						/* translators: 1: the line entered, 2: what is wrong with it */
						__( 'Motion region "%1$s" was not saved: %2$s.', 'fsbhoa-ac-vehicle' ),
						$line,
						$error
					)
				);
				continue;
			}
			$seen[ $parts[0] . ' ' . $parts[1] ] = true;
			$regions[] = array(
				'camera' => $parts[0],
				'name'   => $parts[1],
				'left'   => $edges[0],
				'top'    => $edges[1],
				'right'  => $edges[2],
				'bottom' => $edges[3],
			);
			if ( '' !== $kind ) {
				$regions[ count( $regions ) - 1 ]['kind'] = $kind;
			}
		}
		return $regions;
	}

	/**
	 * The saved regions as the field's text, one a line.
	 */
	private static function motion_regions_text( $regions ) {
		$lines = array();
		foreach ( (array) $regions as $r ) {
			$lines[] = trim( implode( ' ', array( $r['camera'], $r['name'], $r['left'], $r['top'], $r['right'], $r['bottom'], $r['kind'] ?? '' ) ) );
		}
		return implode( "\n", $lines );
	}

	/**
	 * Parses the Car Count Zones text, one zone a line:
	 * "<camera IP> approach|beyond <x1> <y1> <x2> <y2> <x3> <y3> ...", a polygon of 3 to 12
	 * corners in percent of the picture. A camera counts cars only with both zones. A line
	 * that doesn't fit is dropped with a message.
	 *
	 * @return array[] camera, zone, points (array of array( x, y ))
	 */
	private function sanitize_count_zones( $text, $options ) {
		if ( is_array( $text ) ) { // already parsed (an option saved before)
			$text = self::count_zones_text( $text );
		}
		// Only the overview cameras: vehicle_service counts from the frames it keeps for their photos
		$cameras = array();
		foreach ( array( 'gates', 'exit_lanes' ) as $table ) {
			foreach ( (array) ( $options[ $table ] ?? array() ) as $lane ) {
				if ( ! empty( $lane['overview_ip'] ) ) {
					$cameras[] = $lane['overview_ip'];
				}
			}
		}
		$zones = array();
		$seen  = array();
		foreach ( preg_split( '/\r\n|\r|\n/', (string) $text ) as $line ) {
			$line = trim( sanitize_text_field( $line ) );
			if ( '' === $line || '#' === $line[0] ) {
				continue;
			}
			$parts  = preg_split( '/\s+/', $line );
			$coords = array_slice( $parts, 2 );
			$error  = '';
			if ( count( $parts ) < 2 || ! in_array( $parts[1], array( 'approach', 'beyond' ), true ) ) {
				$error = __( 'it needs a camera address, then "approach" or "beyond", then the corners', 'fsbhoa-ac-vehicle' );
			} elseif ( ! in_array( $parts[0], $cameras, true ) ) {
				$error = __( 'the camera is not a lane\'s overview camera', 'fsbhoa-ac-vehicle' );
			} elseif ( isset( $seen[ $parts[0] . ' ' . $parts[1] ] ) ) {
				$error = __( 'the camera already has that zone', 'fsbhoa-ac-vehicle' );
			} elseif ( count( $coords ) % 2 || count( $coords ) < 6 || count( $coords ) > 24 ) {
				$error = __( 'a zone needs 3 to 12 corners, each an x and a y', 'fsbhoa-ac-vehicle' );
			} else {
				foreach ( $coords as $c ) {
					if ( ! is_numeric( $c ) || $c < 0 || $c > 100 ) {
						$error = __( 'the corners must be numbers from 0 to 100', 'fsbhoa-ac-vehicle' );
					}
				}
			}
			if ( '' !== $error ) {
				add_settings_error(
					self::OPTION_NAME,
					'fsbhoa_vehicle_count_zone',
					sprintf(
						/* translators: 1: the line entered, 2: what is wrong with it */
						__( 'Car count zone "%1$s" was not saved: %2$s.', 'fsbhoa-ac-vehicle' ),
						$line,
						$error
					)
				);
				continue;
			}
			$seen[ $parts[0] . ' ' . $parts[1] ] = true;
			$points = array();
			for ( $i = 0; $i < count( $coords ); $i += 2 ) {
				$points[] = array( (float) $coords[ $i ], (float) $coords[ $i + 1 ] );
			}
			$zones[] = array(
				'camera' => $parts[0],
				'zone'   => $parts[1],
				'points' => $points,
			);
		}
		foreach ( $zones as $z ) {
			$other = 'approach' === $z['zone'] ? 'beyond' : 'approach';
			if ( ! isset( $seen[ $z['camera'] . ' ' . $other ] ) ) {
				add_settings_error(
					self::OPTION_NAME,
					'fsbhoa_vehicle_count_zone_pair',
					sprintf(
						/* translators: 1: camera address, 2: the missing zone */
						__( 'Camera %1$s has no "%2$s" zone, so it won\'t count cars until it has one.', 'fsbhoa-ac-vehicle' ),
						$z['camera'],
						$other
					),
					'warning'
				);
			}
		}
		return $zones;
	}

	/**
	 * The saved zones as the field's text, one a line.
	 */
	private static function count_zones_text( $zones ) {
		$lines = array();
		foreach ( (array) $zones as $z ) {
			$words = array( $z['camera'], $z['zone'] );
			foreach ( (array) $z['points'] as $p ) {
				$words[] = $p[0];
				$words[] = $p[1];
			}
			$lines[] = implode( ' ', $words );
		}
		return implode( "\n", $lines );
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
					'keypad_ip'   => __( 'Keypad camera', 'fsbhoa-ac-vehicle' ),
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
				esc_html_e( 'One row per entrance lane; each has its own DoorKing controller, loop sensor and cameras. The name is the gate shown in the vehicle log and on the monitor. The RAM account is the account name exactly as RAM sends it (as in RAM\'s account list); it is how a DoorKing record is matched to its lane. The addresses are the devices\' IP addresses: only they may call vehicle_service\'s webhooks, and the address tells which lane a call is from. The overview camera sees vehicles pass the gate (and the arm); the keypad camera sees them at the keypad and tag reader. Both are optional.', 'fsbhoa-ac-vehicle' );
			} else {
				esc_html_e( 'One row per exit lane. Exit lanes have no DoorKing controller or loop sensor; every plate read on one is logged as a vehicle leaving, under the lane\'s name. A car may enter at any entrance and leave at any exit, so exits aren\'t tied to an entrance. The name may repeat an entrance\'s name (e.g. South). The overview camera is optional.', 'fsbhoa-ac-vehicle' );
			}
			echo ' ';
			esc_html_e( 'Fill in the blank row to add a lane; clear a lane\'s name to remove it. A device address may be used only once, in either table.', 'fsbhoa-ac-vehicle' );
			?>
		</p>
		<?php
	}

	public function render_sources_section_description() {
		echo '<p>' . esc_html__( 'The entrance and exit lanes, and the devices allowed to send inputs to vehicle_service. Every input is recorded in the raw-input journal (/var/lib/fsbhoa/vehicle_journal), tagged with its lane. This server itself is always allowed. Changes apply as soon as the settings are saved, without restarting vehicle_service.', 'fsbhoa-ac-vehicle' ) . '</p>';
	}

	public function render_ingest_section_description() {
		echo '<p>' . esc_html__( 'vehicle_service posts each finished vehicle event to core\'s vehicle log (POST /vehicle-event), which checks the API key. Photo retention is a Live Monitor setting in core.', 'fsbhoa-ac-vehicle' ) . '</p>';
	}

	public function render_daemon_section_description() {
		echo '<p>' . esc_html__( 'vehicle_service runs on this machine and listens on one port, on all interfaces. Changing it takes effect when vehicle_service is restarted (sudo systemctl restart fsbhoa_vehicle); the devices pointing at the old port must be changed too.', 'fsbhoa-ac-vehicle' ) . '</p>';
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
			<?php esc_html_e( 'HTTP port (default 8088). The gate devices call in here: each loop sensor Shelly\'s webhook (/webhook/loop?state=on or off), and cameras if they push. Only the lane tables\' device addresses may call. This page reads vehicle_service\'s health here.', 'fsbhoa-ac-vehicle' ); ?>
		</p>
		<?php
	}

	public function render_ram_file_field() {
		$options = self::get_options();
		?>
		<input type="text"
				 id="fsbhoa_vehicle_ram_file"
				 name="<?php echo esc_attr( self::OPTION_NAME . '[ram_file]' ); ?>"
				 value="<?php echo esc_attr( $options['ram_file'] ); ?>"
				 placeholder="<?php echo esc_attr( self::default_ram_file() ); ?>"
				 class="large-text code" />
		<p class="description">
			<?php
			printf(
				/* translators: %s: suggested path */
				esc_html__( 'Where this server reads RAM\'s Live Streaming "Disk File" (suggested: %s; on the RAM PC that folder is Y:\Automation\DoorKing\Live_...). DoorKing records come only from this file: it keeps recording while vehicle_service is down, and vehicle_service continues where it stopped. In RAM, give each account a Disk File destination (Raw Text) at this file, and no IP Address destination. The folder name must contain this server\'s environment. vehicle_service renames the file daily (stream_YYYY-MM-DD.txt) and deletes old ones with the journal. Empty: no DoorKing records are read. Restart vehicle_service after changing this.', 'fsbhoa-ac-vehicle' ),
				'<code>' . esc_html( self::default_ram_file() ) . '</code>'
			);
			?>
		</p>
		<?php
	}

	public function render_lpr_drop_dir_field() {
		$options = self::get_options();
		?>
		<input type="text"
				 id="fsbhoa_vehicle_lpr_drop_dir"
				 name="<?php echo esc_attr( self::OPTION_NAME . '[lpr_drop_dir]' ); ?>"
				 value="<?php echo esc_attr( $options['lpr_drop_dir'] ); ?>"
				 placeholder="<?php echo esc_attr( self::LPR_DROP_DIR_DEFAULT ); ?>"
				 class="large-text code" />
		<p class="description">
			<?php esc_html_e( 'Where the LPR cameras\' FTP uploads land: plate crops, scene photos and hourly summaries, in a folder per camera (its MAC address). The FTP server (vsftpd, port 2121, user specolpr) is set up by lpr_ftp/setup.sh in this plugin, which makes this folder the upload account\'s home; in each camera, set Network > TCP/IP > FTP to this server, port 2121, path /. Each read is recorded in the journal; for now nothing is sent to the vehicle log. Uploads are deleted after 7 days. Restart vehicle_service after changing this.', 'fsbhoa-ac-vehicle' ); ?>
		</p>
		<?php
	}

	public function render_motion_regions_field() {
		$options = self::get_options();
		?>
		<textarea id="fsbhoa_vehicle_motion_regions"
				 name="<?php echo esc_attr( self::OPTION_NAME . '[motion_regions]' ); ?>"
				 rows="5"
				 class="large-text code"
				 placeholder="192.168.107.131 gate 54 20 82 50"><?php echo esc_textarea( self::motion_regions_text( $options['motion_regions'] ) ); ?></textarea>
		<p class="description">
			<?php esc_html_e( 'Areas of the overview and keypad cameras\' pictures that vehicle_service watches for motion, one a line: the camera\'s address (a lane\'s overview or keypad camera), a name (gate, arm, keypad), then the left, top, right and bottom edges in percent of the picture\'s width and height. A region ending with "bright" isn\'t watched for motion: it is on while enough of it is bright (the white arm against the hedge when it is up), and each change is recorded in the journal. It compares the camera\'s snapshot once a second; each pass of motion is recorded in the journal, and its frames are kept, half size, for 3 days in /var/lib/fsbhoa/overview_frames. Applied without a restart.', 'fsbhoa-ac-vehicle' ); ?>
		</p>
		<?php
	}

	public function render_count_zones_field() {
		$options = self::get_options();
		?>
		<textarea id="fsbhoa_vehicle_count_zones"
				 name="<?php echo esc_attr( self::OPTION_NAME . '[count_zones]' ); ?>"
				 rows="3"
				 class="large-text code"
				 placeholder="192.168.107.141 approach 18 100 30 62 42 45 70 45 66 63 40 100"><?php echo esc_textarea( self::count_zones_text( $options['count_zones'] ?? array() ) ); ?></textarea>
		<p class="description">
			<?php esc_html_e( 'Two areas of a lane\'s overview camera that vehicle_service counts cars with, one a line: the camera\'s address, "approach" (the lane just before the arm) or "beyond" (past the arm, where a car has come in), then the corners of the area, each an x and a y in percent of the picture\'s width and height (3 to 12 corners). A car is counted when "beyond" fills after "approach" did; a car that leaves "approach" without reaching "beyond" backed out. Each is recorded in the journal. It uses the frames kept for the overview photos, twice a second. Applied without a restart.', 'fsbhoa-ac-vehicle' ); ?>
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

	public function render_plate_events_field() {
		$options = self::get_options();
		?>
		<label for="fsbhoa_vehicle_plate_events">
			<input type="checkbox"
				 id="fsbhoa_vehicle_plate_events"
				 name="<?php echo esc_attr( self::OPTION_NAME . '[plate_events]' ); ?>"
				 value="1"
				 <?php checked( 1, $options['plate_events'] ); ?> />
			<?php esc_html_e( 'Log each LPR camera read (entrance and exit lanes) as a vehicle event on its own, with the plate crop and the scene photo, so every vehicle the cameras see shows in the vehicle log and on the monitor. Vehicles without a readable plate show as "NO PLATE". Not matched with DoorKing credentials: a stopgap until correlation is built. Applied without a restart.', 'fsbhoa-ac-vehicle' ); ?>
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
	 * Reads the RAM PC's heartbeat: ram_pc_status.txt, which DoorKing's DK_Sync.ahk rewrites
	 * every minute in the RAM Stream File's folder. The file name is a contract with that
	 * script (fsbhoa_ac_doorking's CLAUDE.md, "RAM automation scripts"). Returns null when
	 * there is no file, else its key=value lines, plus 'file_time' (when the file was last
	 * written, by the NAS's clock, so the RAM PC's clock doesn't matter).
	 *
	 * @param string $ram_file The RAM Stream File path.
	 */
	private static function read_ram_pc_status( $ram_file ) {
		$path = dirname( $ram_file ) . '/' . self::RAM_PC_STATUS_FILE;
		if ( '' === $ram_file || ! is_readable( $path ) ) {
			return null;
		}
		clearstatcache( true, $path );
		$status = array();
		foreach ( (array) file( $path, FILE_IGNORE_NEW_LINES | FILE_SKIP_EMPTY_LINES ) as $line ) {
			$parts = explode( '=', trim( $line ), 2 );
			if ( 2 === count( $parts ) ) {
				$status[ trim( $parts[0] ) ] = trim( $parts[1] );
			}
		}
		$status['file_time'] = (int) filemtime( $path ); // after the file's lines, so none can replace it
		return $status;
	}

	/**
	 * Shows where DoorKing records come from and whether RAM is running, and when each kind
	 * of input last arrived.
	 *
	 * DoorKing records come from RAM's Disk File (the RAM Stream File setting). A quiet file
	 * may just mean no traffic, so the RAM PC's heartbeat shows that RAM and DK_Sync.ahk are
	 * running.
	 *
	 * @param array $health The decoded /health response.
	 */
	private function render_input_status( $health ) {
		$last_seen = isset( $health['last_seen'] ) && is_array( $health['last_seen'] ) ? $health['last_seen'] : array();
		$uptime    = isset( $health['uptime'] ) ? absint( $health['uptime'] ) : 0;
		$options   = self::get_options();
		$file      = is_array( $health['ram_file'] ?? null ) ? $health['ram_file'] : null;
		$pc        = self::read_ram_pc_status( (string) $options['ram_file'] );
		$pc_stale  = $pc && ( time() - $pc['file_time'] > self::RAM_PC_STATUS_STALE );
		$problem   = ! $file || ! empty( $file['error'] ) || ( $pc && ( $pc_stale || '1' !== ( $pc['ram_running'] ?? '' ) ) );

		$format = function ( $iso ) {
			$ts = is_int( $iso ) ? $iso : strtotime( (string) $iso );
			if ( ! $ts ) {
				return '';
			}
			/* translators: 1: date and time, 2: how long ago */
			return sprintf( __( '%1$s (%2$s ago)', 'fsbhoa-ac-vehicle' ), wp_date( 'M j, g:i:s a', $ts ), human_time_diff( $ts ) );
		};
		?>
		<div class="notice notice-<?php echo $problem ? 'error' : 'info'; ?> inline" style="margin: 15px 0;">
			<p><strong><?php esc_html_e( 'DoorKing RAM Live Transactions', 'fsbhoa-ac-vehicle' ); ?></strong></p>
			<?php if ( $file ) : ?>
				<p>
					<?php
					printf(
						/* translators: 1: file path, 2: last record time, 3: last read time */
						esc_html__( 'Reading %1$s. Last record read: %2$s. Last checked: %3$s.', 'fsbhoa-ac-vehicle' ),
						'<code>' . esc_html( $file['path'] ?? '' ) . '</code>',
						esc_html( ! empty( $file['last_record_at'] ) ? $format( $file['last_record_at'] ) : __( 'none since vehicle_service started', 'fsbhoa-ac-vehicle' ) ),
						esc_html( ! empty( $file['last_poll_at'] ) ? $format( $file['last_poll_at'] ) : __( 'not yet', 'fsbhoa-ac-vehicle' ) )
					);
					?>
				</p>
				<?php if ( ! empty( $file['error'] ) ) : ?>
					<p><strong><?php esc_html_e( 'Cannot read the file:', 'fsbhoa-ac-vehicle' ); ?></strong> <code><?php echo esc_html( $file['error'] ); ?></code> <?php esc_html_e( 'vehicle_service retries twice a second and continues where it stopped, so nothing is lost while RAM keeps writing it.', 'fsbhoa-ac-vehicle' ); ?></p>
				<?php endif; ?>
			<?php elseif ( '' !== $options['ram_file'] ) : ?>
				<p><strong><?php esc_html_e( 'The RAM Stream File is set but vehicle_service is not reading it, so no DoorKing records arrive.', 'fsbhoa-ac-vehicle' ); ?></strong> <?php esc_html_e( 'Restart vehicle_service after changing it; if it still isn\'t read, its log says why (journalctl -u fsbhoa_vehicle).', 'fsbhoa-ac-vehicle' ); ?></p>
			<?php else : ?>
				<p><strong><?php esc_html_e( 'No RAM Stream File is set (Gate Devices, below), so no DoorKing records arrive.', 'fsbhoa-ac-vehicle' ); ?></strong></p>
			<?php endif; ?>

			<?php if ( $pc ) : ?>
				<p>
					<?php
					printf(
						/* translators: 1: PC name, 2: time of the last heartbeat, 3: whether RAM runs */
						esc_html__( 'RAM PC %1$s: last heartbeat %2$s; RAM %3$s.', 'fsbhoa-ac-vehicle' ),
						'<code>' . esc_html( $pc['host'] ?? '?' ) . '</code>',
						esc_html( $format( $pc['file_time'] ) ),
						'1' === ( $pc['ram_running'] ?? '' ) ? esc_html__( 'is running', 'fsbhoa-ac-vehicle' ) : '<strong>' . esc_html__( 'is NOT running', 'fsbhoa-ac-vehicle' ) . '</strong>'
					);
					if ( $pc_stale ) {
						echo ' <strong>' . esc_html__( 'The heartbeat is old: DK_Sync.ahk or the RAM PC may be down.', 'fsbhoa-ac-vehicle' ) . '</strong>';
					}
					?>
				</p>
			<?php elseif ( '' !== $options['ram_file'] ) : ?>
				<p><?php esc_html_e( "No heartbeat from the RAM PC (ram_pc_status.txt in the file's folder). DK_Sync.ahk writes it every minute when its RamWatch setting is on; without it, this page can't tell whether RAM is running.", 'fsbhoa-ac-vehicle' ); ?></p>
			<?php endif; ?>

			<p>
				<?php
				$labels = array(
					'ram_record'      => __( 'DoorKing record', 'fsbhoa-ac-vehicle' ),
					'loop'            => __( 'Loop change', 'fsbhoa-ac-vehicle' ),
					'plate'           => __( 'Plate read', 'fsbhoa-ac-vehicle' ),
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
			'api_key'              => get_option( 'fsbhoa_ac_verify_api_key', '' ),
			'correlation_window'   => absint( $options['correlation_window'] ?? 15 ),
			'enable_debug_logging' => ! empty( $options['enable_debug_logging'] ) ? 1 : 0,
			'doorking_events'      => ! empty( $options['doorking_events'] ),
			'plate_events'         => ! empty( $options['plate_events'] ),
			'wordpress_host'       => sanitize_text_field( $wp_host ? $wp_host : '127.0.0.1' ),
			'environment'          => sanitize_text_field( $environment ),
			'ram_file'             => sanitize_text_field( $options['ram_file'] ?? '' ),
			'lpr_drop_dir'         => sanitize_text_field( $options['lpr_drop_dir'] ?? self::LPR_DROP_DIR_DEFAULT ),
			'gates'                => array_values( is_array( $options['gates'] ?? null ) ? $options['gates'] : array() ),
			'exit_lanes'           => array_values( is_array( $options['exit_lanes'] ?? null ) ? $options['exit_lanes'] : array() ),
			'motion_regions'       => array_values( is_array( $options['motion_regions'] ?? null ) ? $options['motion_regions'] : array() ),
			'count_zones'          => array_values( is_array( $options['count_zones'] ?? null ) ? $options['count_zones'] : array() ),
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
