import http from "k6/http";
import { check, sleep } from "k6";

http.setResponseCallback(http.expectedStatuses(200, 201, 409, 429));

const BASE_URL = __ENV.BASE_URL || "http://localhost:8080";
const API_KEY = __ENV.API_KEY || "";
const MODEL = __ENV.MODEL || "deepseek-chat";

export const options = {
  scenarios: {
    normal_traffic: {
      executor: "ramping-vus",
      startVUs: 0,
      stages: [
        { duration: "30s", target: 50 },
        { duration: "1m", target: 50 },
        { duration: "30s", target: 100 },
        { duration: "1m", target: 100 },
        { duration: "30s", target: 0 },
      ],
    },
  },
  thresholds: {
    http_req_duration: ["p(95)<500", "p(99)<1000"],
    http_req_failed: ["rate<0.01"],
  },
};

export default function () {
  const health = http.get(`${BASE_URL}/healthz`);
  check(health, { "health 200": r => r.status === 200 });

  if (API_KEY) {
    const payload = JSON.stringify({
      model: MODEL,
      messages: [{ role: "user", content: "Say hello in 5 words" }],
    });
    const headers = { "Content-Type": "application/json", "X-API-Key": API_KEY };
    const res = http.post(`${BASE_URL}/v1/chat/completions`, payload, { headers });
    check(res, {
      "gateway status is 200 or 429": r => r.status === 200 || r.status === 429,
    });
  } else {
    const username = `loadtest_${__VU}_${__ITER}`;
    const reg = http.post(`${BASE_URL}/api/register`,
      JSON.stringify({ username, password: "testpass123" }),
      { headers: { "Content-Type": "application/json" } });
    check(reg, { "register OK": r => r.status === 201 || r.status === 409 });

    const login = http.post(`${BASE_URL}/api/login`,
      JSON.stringify({ username, password: "testpass123" }),
      { headers: { "Content-Type": "application/json" } });
    check(login, { "login 200": r => r.status === 200 });
  }

  sleep(0.1);
}
