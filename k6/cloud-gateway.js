import http from "k6/http";
import { check, sleep } from "k6";

const BASE_URL = "http://localhost:8080";
const API_KEY = "sk-8c7c47bd-cf4c-4c81-81ac-efc23f0a";

export const options = {
  vus: 50,
  duration: "30s",
  thresholds: {
    http_req_failed: ["rate<0.01"],
  },
};

export default function () {
  const payload = JSON.stringify({
    model: "gpt-3.5-turbo",
    messages: [{ role: "user", content: "test message for load testing" }],
  });
  const headers = { "Content-Type": "application/json", "X-API-Key": API_KEY };
  const res = http.post(`${BASE_URL}/v1/chat/completions`, payload, { headers });
  check(res, { "status 200": r => r.status === 200 });
  sleep(0.01);
}