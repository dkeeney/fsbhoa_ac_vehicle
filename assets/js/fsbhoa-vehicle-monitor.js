(function() {
    document.addEventListener('DOMContentLoaded', function() {
        const vehicleEventList = document.getElementById('vehicle-event-list');
        if (!vehicleEventList) return;

        let vehiclePlaceholder = document.getElementById('vehicle-log-placeholder');
        let lastKnownId = 0;

        function createCard(event) {
           const li = document.createElement('li');
           li.dataset.logId = event.vehicle_log_id;

           const isCircumvention = parseInt(event.is_circumvention, 10) === 1;
           const borderStyle = isCircumvention
               ? 'border-left: 4px solid #ef4444; background: #fef2f2;'
               : 'border-left: 4px solid #3b82f6; background: #eff6ff;';

           const plate = event.lpr_plate_string || 'NO PLATE';
           const gate = event.gate_identifier || 'Vehicle Gate';
           const auth = event.auth_id
               ? ('Auth: ' + event.auth_id)
               : (isCircumvention ? 'NO CREDENTIAL' : 'No PIN/Card');
           const time = event.event_timestamp ? event.event_timestamp.split(' ')[1] : '';

           const thumbUrl = '/wp-json/fsbhoa/v1/vehicle-image/' + event.vehicle_log_id + '?type=lpr';
           const contextUrl = '/wp-json/fsbhoa/v1/vehicle-image/' + event.vehicle_log_id + '?type=context';

           let warnBanner = '';
           if (isCircumvention) {
               warnBanner = '<div style="background: #fee2e2; color: #991b1b; padding: 2px 8px; border-radius: 4px; font-weight: bold; font-size: 11px; margin-top: 4px; display: inline-block;">&#9888; Gate Tripped Without Auth</div>';
           }

           let imgBox = '';
           if (event.has_lpr_img == 1 || event.has_lpr_img === true) {
               imgBox = '<div style="flex-shrink: 0; cursor: pointer; text-align: center;" onclick="openVehicleScene(\'' + contextUrl + '\')" title="Click to view full context scene">' +
                  '<img src="' + thumbUrl + '" alt="' + plate + '" style="width: 80px; height: 50px; object-fit: cover; border-radius: 4px; border: 1px solid #cbd5e1; background: #e2e8f0;" onerror="this.style.display=\'none\';">' +
                 '<div style="font-size: 9px; color: #64748b; margin-top: 2px;">Expand</div>' +
               '</div>';
           }

           let cardholderHtml = '';
           const chId = parseInt(event.cardholder_id, 10);
           if (chId > 0) {
               cardholderHtml = '<div style="margin-top: 4px;">' +
                  '<a href="#" onclick="if(window.showCardholderSummaryModal){window.showCardholderSummaryModal(' + chId + ');} return false;" style="font-size: 13px; font-weight: 600; color: #1d4ed8; text-decoration: underline; cursor: pointer;">' +
                  (event.cardholder_name || 'View Cardholder') +
                  '</a>' +
               '</div>';
           } else if (event.cardholder_name) {
               cardholderHtml = '<div style="font-size: 12px; color: #64748b; margin-top: 4px;">' + event.cardholder_name + '</div>';
           }

           li.setAttribute('style', 'padding: 16px; display: flex; gap: 16px; align-items: flex-start; ' + borderStyle);
           li.innerHTML = imgBox +
               '<div style="flex: 1; min-width: 0;">' +
                  '<div style="display: flex; justify-content: space-between; align-items: baseline;">' +
                    '<span style="font-size: 16px; font-weight: bold; font-family: monospace; background: #f1f5f9; padding: 2px 6px; border-radius: 4px; border: 1px solid #cbd5e1; color: #0f172a;">' + plate + '</span>' +
                    '<time style="font-size: 12px; color: #64748b; font-family: monospace;">' + time + '</time>' +
                  '</div>' +
                  '<div style="font-size: 13px; color: #334155; margin-top: 4px; font-weight: 500;">' +
                    gate + ' &#x2022; <span style="color: #64748b;">' + auth + '</span>' +
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
