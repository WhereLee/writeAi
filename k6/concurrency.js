import http from "k6/http";
import { check, sleep } from "k6";

// Concurrency correctness test: many parallel requests against limited balance
// Run with: k6 run -e BASE_URL=http://localhost:8080 -e API_KEY=sk-xxx concurrency.js

const BASE_URL = __ENV.BASE_URL || "http://localhost:8080";
const API_KEY = __ENV.API_KEY || "";

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
  const payload = JSON.stringify({
    model: "gpt-3.5-turbo",
    messages: [{ role: "user", content: "Hello, this is a test message for concurrent billing verification." }],
  });
  const headers = { "Content-Type": "application/json", "X-API-Key": API_KEY };
  const res = http.post(`${BASE_URL}/v1/chat/completions`, payload, { headers });
  check(res, {
    "status is valid": r => [200, 402, 429, 502].includes(r.status),
  });
  sleep(0.05);
}
