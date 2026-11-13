import http from "k6/http";
import { check, sleep } from "k6";

http.setResponseCallback(http.expectedStatuses(200, 429));

const BASE_URL = __ENV.BASE_URL || "http://localhost:8080";
const API_KEY = __ENV.API_KEY || "";
const MODEL = __ENV.MODEL || "deepseek-chat";

if (!API_KEY) {
  throw new Error("Set API_KEY before running this test");
}

export const options = {
  vus: Number(__ENV.VUS || 50),
  duration: __ENV.DURATION || "30s",
  thresholds: {
    http_req_failed: ["rate<0.01"],
  },
};

export default function () {
  const payload = JSON.stringify({
    model: MODEL,
    messages: [{ role: "user", content: "test message for load testing" }],
  });
  const headers = { "Content-Type": "application/json", "X-API-Key": API_KEY };
  const res = http.post(`${BASE_URL}/v1/chat/completions`, payload, { headers });
  check(res, { "status 200 or 429": r => r.status === 200 || r.status === 429 });
  sleep(0.01);
}
