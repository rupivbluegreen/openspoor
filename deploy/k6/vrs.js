// k6 load test: VRS responder (GS1 Lightweight Messaging verification).
// Budget (non-negotiable 6): VRS response p95 < 1 s.
//   BASE_URL=http://localhost:8080 API_KEY=osk_... TOKEN=<jwt> GTIN=09506000134352 k6 run deploy/k6/vrs.js
import http from 'k6/http';
import { check } from 'k6';

export const options = {
  scenarios: {
    vrs: { executor: 'constant-arrival-rate', rate: Number(__ENV.RATE || 200), timeUnit: '1s', duration: __ENV.DURATION || '60s',
      preAllocatedVUs: 50, maxVUs: 200 },
  },
  thresholds: {
    'http_req_duration{name:vrs}': ['p(95)<1000'],
    'checks': ['rate>0.99'],
  },
};

const BASE = __ENV.BASE_URL || 'http://localhost:8080';

export function setup() {
  const res = http.get(`${BASE}/api/v1/trace/state?gtin=${__ENV.GTIN}&limit=1000`, { headers: { Authorization: `Bearer ${__ENV.TOKEN}` } });
  const serials = res.json('items').map((s) => s.serial).filter(Boolean);
  if (serials.length === 0) throw new Error('no serials found; run `openspoor seed` first');
  return { serials };
}

export default function (data) {
  const serial = data.serials[Math.floor(Math.random() * data.serials.length)];
  const res = http.post(`${BASE}/api/v1/vrs/verify`, JSON.stringify({
    gtin: __ENV.GTIN, serialNumber: serial, requestorGLN: '0614141000005', context: 'dscsaSaleableReturn',
  }), { headers: { 'Content-Type': 'application/json', 'X-API-Key': __ENV.API_KEY }, tags: { name: 'vrs' } });
  check(res, { 'answered (200)': (r) => r.status === 200, 'verified': (r) => r.json('data.verified') === true });
}
