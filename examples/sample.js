import http from 'k6/http';
import { sleep } from 'k6';

export const options = {
  vus: 2,
  iterations: 10,
};

export default function () {
  let resp = http.get('http://localhost:8000');
  debugger;
  let status = resp.status;
  console.log('status: ' + status);
  let body = resp.body;
  sleep(1);
}
