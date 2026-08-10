import { proxyApiRequest } from "../../../src/server/apiProxy.js";

export const runtime = "nodejs";
export const dynamic = "force-dynamic";

export function GET(request: Request) {
  return proxyApiRequest(request);
}

export function HEAD(request: Request) {
  return proxyApiRequest(request);
}

export function OPTIONS(request: Request) {
  return proxyApiRequest(request);
}

export function POST(request: Request) {
  return proxyApiRequest(request);
}

export function PUT(request: Request) {
  return proxyApiRequest(request);
}

export function PATCH(request: Request) {
  return proxyApiRequest(request);
}

export function DELETE(request: Request) {
  return proxyApiRequest(request);
}
