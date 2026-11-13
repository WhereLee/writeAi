import http from "k6/http";
import { check, sleep } from "k6";

http.setResponseCallback(http.expectedStatuses(200, 402, 429, 502));

const BASE_URL = __ENV.BASE_URL || "http://localhost:8080";
const API_KEY = __ENV.API_KEY || "";
const MODEL = __ENV.MODEL || "deepseek-chat";

export const options = {
  scenarios: {
    concurrent_billing: {
      executor: "shared-iterations",
      vus: 20,
      iterations: 100,
      maxDuration: "2m",
    },
  },
  thresholds: {
    http_req_failed: ["rate<0.05"],
  },
};

export default function () {
  if (!API_KEY) {
    console.log("Set API_KEY env var to run this test");
    return;
  }
  const requestID = `${__VU}-${__ITER}-${Date.now()}`;
  const payload = JSON.stringify({
    model: MODEL,
    messages: [{ role: "user", content: "Hello, this is a test message for concurrent billing verification." }],
  });
  const headers = {
    "Content-Type": "application/json",
    "X-API-Key": API_KEY,
    "X-Request-Id": requestID,
  };
  const res = http.post(`${BASE_URL}/v1/chat/completions`, payload, { headers });
  check(res, {
    "status is valid": r => [200, 402, 429, 502].includes(r.status),
  });
  sleep(0.05);
}
