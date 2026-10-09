-- ==============================================================================
-- FSBHOA AC Vehicle: vehicle access log
--
-- Reference copy. The plugin creates and upgrades this table itself
-- (includes/class-fsbhoa-vehicle-db.php); keep the two in step.
-- ==============================================================================

CREATE TABLE IF NOT EXISTS `ac_vehicle_log` (
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
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;
