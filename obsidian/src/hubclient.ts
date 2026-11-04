import type {
  Changes,
  DeleteResult,
  Hello,
  Manifest,
  PutResult,
  StatusReport,
} from "./protocol";

export class HubError extends Error {
  readonly status: number;
  readonly code: string;
  constructor(status: number, code: string, message: string) {
    super(`${code}: ${message}`);
    this.name = "HubError";
    this.status = status;
    this.code = code;
  }
}

export class NetworkError extends Error {
  constructor(message: string) {
    super(message);
    this.name = "NetworkError";
  }
}

export interface RequestParam {
  url: string;
  method?: string;
  headers?: Record<string, string>;
  body?: string | ArrayBuffer | Uint8Array;
}

export interface ObsidianResponseLike {
  status: number;
  headers: Record<string, string>;
  arrayBuffer?: ArrayBuffer;
  text?: string;
  json?: unknown;
}

export function adaptObsidianResponse(res: ObsidianResponseLike): RequestResponse {
  const adapted: RequestResponse = {
    status: res.status,
    headers: res.headers,
    arrayBuffer: res.arrayBuffer,
    body: res.arrayBuffer ? new Uint8Array(res.arrayBuffer) : undefined,
  };
  Object.defineProperty(adapted, "text", {
    get() {
      return res.text;
    },
    enumerable: true,
    configurable: true,
  });
  return adapted;
}

export interface RequestResponse {
  status: number;
  headers: Record<string, string>;
  arrayBuffer?: ArrayBuffer;
  body?: Uint8Array;
  json?: unknown;
  text?: string;
}

export type RequestFn = (param: RequestParam) => Promise<RequestResponse>;

export interface HubClientOptions {
  baseUrl: string;
  deviceId: string;
  deviceName: string;
  request: RequestFn;
}

export class HubClient {
  constructor(public readonly options: HubClientOptions) {}

  private getBaseUrl(): string {
    return this.options.baseUrl.replace(/\/+$/, "");
  }

  private async request(
    path: string,
    method = "GET",
    body?: string | ArrayBuffer | Uint8Array,
    extraHeaders?: Record<string, string>
  ): Promise<RequestResponse> {
    const fullUrl = `${this.getBaseUrl()}${path.startsWith("/") ? path : `/${path}`}`;
    const headers: Record<string, string> = {
      "X-LW-Device": this.options.deviceId,
      "X-LW-Device-Name": this.options.deviceName,
      ...extraHeaders,
    };

    let res: RequestResponse;
    try {
      res = await this.options.request({
        url: fullUrl,
        method,
        headers,
        body,
      });
    } catch (err: unknown) {
      if (err instanceof HubError) {
        throw err;
      }
      throw new NetworkError(err instanceof Error ? err.message : String(err));
    }

    if (res.status < 200 || res.status >= 300) {
      let code = "internal";
      let message = `HTTP ${res.status}`;
      if (typeof res.text === "string" && res.text.trim().length > 0) {
        try {
          const parsed = JSON.parse(res.text) as { error?: { code?: string; message?: string } };
          if (parsed?.error) {
            code = parsed.error.code || code;
            message = parsed.error.message || message;
          }
        } catch {
          message = res.text;
        }
      } else if (res.json && typeof res.json === "object" && "error" in (res.json as Record<string, unknown>)) {
        const errObj = (res.json as { error: { code?: string; message?: string } }).error;
        if (errObj) {
          code = errObj.code || code;
          message = errObj.message || message;
        }
      }
      throw new HubError(res.status, code, message);
    }

    return res;
  }

  private parseJson<T>(res: RequestResponse): T {
    if (typeof res.text === "string" && res.text.trim().length > 0) {
      return JSON.parse(res.text) as T;
    }
    if (res.json !== undefined && res.json !== null) {
      return res.json as T;
    }
    throw new Error("Empty response body when JSON expected");
  }

  async hello(): Promise<Hello> {
    const res = await this.request("/v1/hello", "GET");
    return this.parseJson<Hello>(res);
  }

  async manifest(): Promise<Manifest> {
    const res = await this.request("/v1/manifest", "GET");
    return this.parseJson<Manifest>(res);
  }

  async getFile(path: string): Promise<{ data: Uint8Array; sha256: string }> {
    const res = await this.request(`/v1/file?path=${encodeURIComponent(path)}`, "GET");
    let sha256 = "";
    if (res.headers) {
      for (const [k, v] of Object.entries(res.headers)) {
        if (k.toLowerCase() === "x-lw-sha256") {
          sha256 = v;
          break;
        }
      }
    }

    let data: Uint8Array;
    if (res.body instanceof Uint8Array) {
      data = res.body;
    } else if (res.arrayBuffer) {
      data = new Uint8Array(res.arrayBuffer);
    } else if (typeof res.text === "string") {
      data = new TextEncoder().encode(res.text);
    } else {
      data = new Uint8Array(0);
    }

    return { data, sha256 };
  }

  async putFile(path: string, base: string, body: Uint8Array): Promise<PutResult> {
    const res = await this.request(
      `/v1/file?path=${encodeURIComponent(path)}&base=${encodeURIComponent(base)}`,
      "PUT",
      body,
      { "Content-Type": "application/octet-stream" }
    );
    return this.parseJson<PutResult>(res);
  }

  async deleteFile(path: string, base: string): Promise<DeleteResult> {
    const res = await this.request(
      `/v1/file?path=${encodeURIComponent(path)}&base=${encodeURIComponent(base)}`,
      "DELETE"
    );
    return this.parseJson<DeleteResult>(res);
  }

  async changes(since: string): Promise<Changes> {
    const res = await this.request(`/v1/changes?since=${encodeURIComponent(since)}`, "GET");
    return this.parseJson<Changes>(res);
  }

  async postStatus(status: StatusReport): Promise<void> {
    await this.request("/v1/status", "POST", JSON.stringify(status), {
      "Content-Type": "application/json",
    });
  }
}
