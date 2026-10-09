(function() {
    document.addEventListener('DOMContentLoaded', function() {
        const vehicleEventList = document.getElementById('vehicle-event-list');
        if (!vehicleEventList) return;

        let vehiclePlaceholder = document.getElementById('vehicle-log-placeholder');
        let lastKnownId = 0;

        // Escape text from the server before it goes into innerHTML.
        function esc(value) {
           return String(value == null ? '' : value)
               .replace(/&/g, '&amp;')
               .replace(/</g, '&lt;')
               .replace(/>/g, '&gt;')
               .replace(/"/g, '&quot;')
               .replace(/'/g, '&#39;');
        }

        function isTrue(value) {
           return value == 1 || value === true;
        }

        // event_timestamp is local time "YYYY-MM-DD HH:MM:SS[.mmm]"; show it like the
        // pedestrian log does ("3:07:42 PM").
        function formatTime(timestamp) {
           const m = /^\d{4}-\d{2}-\d{2} (\d{2}):(\d{2}):(\d{2})/.exec(timestamp || '');
           if (!m) return '';
           const hour = parseInt(m[1], 10);
           return ((hour % 12) || 12) + ':' + m[2] + ':' + m[3] + ' ' + (hour < 12 ? 'AM' : 'PM');
        }

        function createCard(event) {
           const li = document.createElement('li');
           const logId = parseInt(event.vehicle_log_id, 10);
           li.dataset.logId = logId;

           const isCircumvention = parseInt(event.is_circumvention, 10) === 1;
           const borderStyle = isCircumvention
               ? 'border-left: 4px solid #ef4444; background: #fef2f2;'
               : 'border-left: 4px solid #3b82f6; background: #eff6ff;';

           const plate = event.lpr_plate_string || 'NO PLATE';
           const gate = event.gate_identifier || 'Vehicle Gate';
           const authPrefixes = {
               DK_WINDSHIELD: 'Tag ',
               DK_ENTRY_CODE: 'Gate Code #',
               DK_DIR_CODE: 'DIR: '
           };
           const auth = event.auth_id
               ? ((authPrefixes[event.auth_type] || 'Auth: ') + event.auth_id)
               : (isCircumvention ? 'NO CREDENTIAL' : 'No PIN/Card');
           const time = formatTime(event.event_timestamp);

           const lprUrl = '/wp-json/fsbhoa/v1/vehicle-image/' + logId + '?type=lpr';
           const contextUrl = '/wp-json/fsbhoa/v1/vehicle-image/' + logId + '?type=context';
           const hasLpr = isTrue(event.has_lpr_img);
           const hasContext = isTrue(event.has_context_img);

           let warnBanner = '';
           if (isCircumvention) {
               warnBanner = '<div style="background: #fee2e2; color: #991b1b; padding: 2px 8px; border-radius: 4px; font-weight: bold; font-size: 11px; margin-top: 4px; display: inline-block;">&#9888; Gate Tripped Without Auth</div>';
           }

           // Thumbnail is the plate crop when there is one; clicking opens the scene photo
           // when there is one. With only one image, that image is used for both.
           let imgBox = '';
           if (hasLpr || hasContext) {
               const thumbUrl = hasLpr ? lprUrl : contextUrl;
               const fullUrl = hasContext ? contextUrl : lprUrl;
               imgBox = '<div style="flex-shrink: 0; cursor: pointer; text-align: center;" onclick="openVehicleScene(\'' + fullUrl + '\')" title="' + (hasContext ? 'Click to view full context scene' : 'Click to enlarge plate') + '">' +
                  '<img src="' + thumbUrl + '" alt="' + esc(plate) + '" style="width: 80px; height: 50px; object-fit: cover; border-radius: 4px; border: 1px solid #cbd5e1; background: #e2e8f0;" onerror="this.style.display=\'none\';">' +
                 '<div style="font-size: 9px; color: #64748b; margin-top: 2px;">Expand</div>' +
               '</div>';
           }

           // When a credential value belongs to more than one person (such as a shared
           // household PIN), the server picks one; say how many others there are.
           const otherMatches = (parseInt(event.credential_matches, 10) || 0) - 1;
           const othersNote = otherMatches > 0
               ? ' <span style="font-size: 11px; color: #64748b;" title="This credential belongs to more than one person">(+' + otherMatches + ' other' + (otherMatches > 1 ? 's' : '') + ')</span>'
               : '';

           let cardholderHtml = '';
           const chId = parseInt(event.cardholder_id, 10);
           if (chId > 0) {
               cardholderHtml = '<div style="margin-top: 4px;">' +
                  '<a href="#" onclick="if(window.showCardholderSummaryModal){window.showCardholderSummaryModal(' + chId + ');} return false;" style="font-size: 13px; font-weight: 600; color: #1d4ed8; text-decoration: underline; cursor: pointer;">' +
                  esc(event.cardholder_name || 'View Cardholder') +
                  '</a>' + othersNote +
               '</div>';
           } else if (event.cardholder_name) {
               cardholderHtml = '<div style="font-size: 12px; color: #64748b; margin-top: 4px;">' + esc(event.cardholder_name) + othersNote + '</div>';
           }

           li.setAttribute('style', 'padding: 16px; display: flex; gap: 16px; align-items: flex-start; ' + borderStyle);
           li.innerHTML = imgBox +
               '<div style="flex: 1; min-width: 0;">' +
                  '<div style="display: flex; justify-content: space-between; align-items: baseline;">' +
                    '<span style="font-size: 16px; font-weight: bold; font-family: monospace; background: #f1f5f9; padding: 2px 6px; border-radius: 4px; border: 1px solid #cbd5e1; color: #0f172a;">' + esc(plate) + '</span>' +
                    '<time style="font-size: 12px; color: #64748b; font-family: monospace;">' + esc(time) + '</time>' +
                  '</div>' +
                  '<div style="font-size: 13px; color: #334155; margin-top: 4px; font-weight: 500;">' +
                    esc(gate) + ' &#x2022; <span style="color: #64748b;">' + esc(auth) + '</span>' +
                  '</div>' +
                  cardholderHtml +
                  warnBanner +
               '</div>';
           return li;
        }

        function addEvent(event) {
           if (vehiclePlaceholder) {
               vehiclePlaceholder.remove();
               vehiclePlaceholder = null;
           }
           const id = parseInt(event.vehicle_log_id, 10);
           if (id > lastKnownId) lastKnownId = id;

           const card = createCard(event);
           vehicleEventList.prepend(card);

           while (vehicleEventList.children.length > 50) {
               vehicleEventList.removeChild(vehicleEventList.lastChild);
           }
        }

        async function fetchRecent() {
           try {
               const url = lastKnownId > 0
                  ? ('/wp-json/fsbhoa/v1/vehicle-recent?since=' + lastKnownId)
                  : '/wp-json/fsbhoa/v1/vehicle-recent';
               const resp = await fetch(url);
               if (!resp.ok) return;
               const events = await resp.json();
               if (Array.isArray(events) && events.length > 0) {
                  if (lastKnownId === 0) {
                     for (let i = events.length - 1; i >= 0; i--) {
                        addEvent(events[i]);
                     }
                  } else {
                     events.forEach(addEvent);
                  }
               }
           } catch (err) {
               console.warn('Vehicle monitor fetch error:', err);
           }
        }

        window.openVehicleScene = function(src) {
           const modal = document.getElementById('fsbhoa-vehicle-modal');
           const img = document.getElementById('fsbhoa-modal-img');
           if (modal && img) {
               img.src = src;
               modal.style.display = 'flex';
           }
        };

        fetchRecent();
        setInterval(fetchRecent, 3000);
    });
})();
