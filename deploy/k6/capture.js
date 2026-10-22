// k6 load test: EPCIS capture of 1,000-event documents.
// Budget (non-negotiable 6): capture acknowledgement p95 < 200 ms.
//   BASE_URL=http://localhost:8080 API_KEY=osk_... k6 run deploy/k6/capture.js
import http from 'k6/http';
import { check } from 'k6';

export const options = {
  scenarios: {
    capture: { executor: 'constant-vus', vus: Number(__ENV.VUS || 4), duration: __ENV.DURATION || '60s' },
  },
  thresholds: {
    'http_req_duration{name:capture}': ['p(95)<200'],
    'checks': ['rate>0.99'],
  },
};

const BASE = __ENV.BASE_URL || 'http://localhost:8080';
const EVENTS = Number(__ENV.EVENTS || 1000);

function doc(vu, iter) {
  const t0 = Date.now();
  const events = [];
  for (let i = 0; i < EVENTS; i++) {
    events.push({
      type: 'ObjectEvent', action: 'OBSERVE', bizStep: 'receiving', disposition: 'in_progress',
      eventTime: new Date(t0 + i).toISOString(), eventTimeZoneOffset: '+00:00',
      epcList: [`urn:epc:id:sgtin:0614141.107346.K6${vu}x${iter}x${i}`],
      readPoint: { id: 'urn:epc:id:sgln:0614141.00001.0' },
    });
  }
  return JSON.stringify({ '@context': ['https://ref.gs1.org/standards/epcis/2.0.0/epcis-context.jsonld'],
    type: 'EPCISDocument', schemaVersion: '2.0', creationDate: new Date().toISOString(), epcisBody: { eventList: events } });
}

export default function () {
  const res = http.post(`${BASE}/api/v1/epcis/capture`, doc(__VU, __ITER), {
    headers: { 'Content-Type': 'application/ld+json', 'X-API-Key': __ENV.API_KEY },
    tags: { name: 'capture' },
  });
  check(res, { 'accepted (202)': (r) => r.status === 202 });
}
