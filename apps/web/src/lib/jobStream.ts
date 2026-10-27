import type { Job, LogLine } from "./api";

export type StreamState = "connecting" | "replaying" | "live" | "reconnecting" | "closed";

export interface StreamMessage {
  type: "job" | "log" | "ready";
  job?: Job;
  log?: LogLine;
}

export interface JobStreamHandlers {
  onLog: (line: LogLine) => void;
  onJob: (job: Job) => void;
  onState: (state: StreamState) => void;
}

/**
 * Follows /api/v1/jobs/{id}/stream with automatic reconnection. The cursor
 * (last seq received) survives reconnects, so after a network drop, a page
 * refresh with a restored cursor, or a backend restart, the server replays only
 * what was missed and every line is delivered exactly once.
 */
export class JobStream {
  private ws?: WebSocket;
  private lastSeq: number;
  private attempt = 0;
  private timer?: ReturnType<typeof setTimeout>;
  private stopped = false;

  constructor(
    private jobId: string,
    private handlers: JobStreamHandlers,
    startAfter = 0,
    private makeSocket: (url: string) => WebSocket = (url) => new WebSocket(url),
  ) {
    this.lastSeq = startAfter;
  }

  get cursor() {
    return this.lastSeq;
  }

  start() {
    this.stopped = false;
    this.connect();
  }

  stop() {
    this.stopped = true;
    clearTimeout(this.timer);
    this.ws?.close();
    this.handlers.onState("closed");
  }

  /** Exposed for tests: process one server message. */
  handle(msg: StreamMessage) {
    switch (msg.type) {
      case "log":
        if (msg.log && msg.log.seq > this.lastSeq) {
          this.lastSeq = msg.log.seq;
          this.handlers.onLog(msg.log);
        }
        break;
      case "job":
        if (msg.job) this.handlers.onJob(msg.job);
        break;
      case "ready":
        this.attempt = 0;
        this.handlers.onState("live");
        break;
    }
  }

  private url() {
    const proto = location.protocol === "https:" ? "wss:" : "ws:";
    return `${proto}//${location.host}/api/v1/jobs/${this.jobId}/stream?after=${this.lastSeq}`;
  }

  private connect() {
    this.handlers.onState(this.attempt === 0 ? "connecting" : "reconnecting");
    const ws = this.makeSocket(this.url());
    this.ws = ws;
    ws.onopen = () => this.handlers.onState("replaying");
    ws.onmessage = (ev) => {
      try {
        this.handle(JSON.parse(ev.data as string) as StreamMessage);
      } catch {
        /* ignore malformed frames */
      }
    };
    ws.onclose = () => {
      if (this.stopped || this.ws !== ws) return;
      this.handlers.onState("reconnecting");
      // Exponential backoff with jitter, capped at 15s.
      const delay = Math.min(15000, 500 * 2 ** this.attempt) * (0.75 + Math.random() * 0.5);
      this.attempt++;
      this.timer = setTimeout(() => this.connect(), delay);
    };
  }
}
