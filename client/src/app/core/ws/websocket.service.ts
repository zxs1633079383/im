import { Injectable, OnDestroy, inject } from '@angular/core';
import { Subject, BehaviorSubject } from 'rxjs';
import { AuthService } from '../auth/auth.service';
import {
  WSFrame, WSMessageType,
  PingPayload, PongPayload,
  PushMsgPayload, PushACKPayload,
  SendACKPayload,
  ReadSyncPayload,
} from './websocket.models';

const WS_URL = 'ws://localhost:8080/ws';
const PING_INTERVAL_MS = 15_000;
const RECONNECT_BASE_DELAY_MS = 3_000;
const MAX_RECONNECT_ATTEMPTS = 10;
const DEVICE_ID_KEY = 'im_device_id';

@Injectable({ providedIn: 'root' })
export class WebSocketService implements OnDestroy {
  private auth = inject(AuthService);

  /** Emits true when connected, false when disconnected. */
  readonly connected$ = new BehaviorSubject<boolean>(false);

  /** Emits every inbound push_msg frame payload. */
  readonly pushMsg$ = new Subject<PushMsgPayload>();

  /** Emits every inbound send_ack frame payload. */
  readonly sendAck$ = new Subject<SendACKPayload>();

  /** Emits every inbound pong frame payload. */
  readonly pong$ = new Subject<PongPayload>();

  /** Emits when another device of the same user marks a channel as read. */
  readonly readSync$ = new Subject<ReadSyncPayload>();

  /** Local max seq per channel (channel_id as string → seq). Used in ping payload. */
  readonly channelSeqs: Record<string, number> = {};

  private ws: WebSocket | null = null;
  private pingTimer: ReturnType<typeof setInterval> | null = null;
  private reconnectTimer: ReturnType<typeof setTimeout> | null = null;
  private reconnectAttempts = 0;
  private destroyed = false;

  // ---- Public API ----

  /** Open the WebSocket connection using the current auth token. No-op if already open. */
  connect(): void {
    if (this.ws?.readyState === WebSocket.OPEN) return;

    const token = this.auth.token();
    if (!token) return;

    this.destroyed = false;

    const deviceID = this.getOrCreateDeviceID();
    const url = `${WS_URL}?token=${encodeURIComponent(token)}&device=${encodeURIComponent(deviceID)}`;
    this.ws = new WebSocket(url);

    this.ws.onopen = () => {
      this.reconnectAttempts = 0;
      this.connected$.next(true);
      this.startPing();
    };

    this.ws.onmessage = (event: MessageEvent) => this.onMessage(event.data as string);

    this.ws.onclose = () => {
      this.connected$.next(false);
      this.stopPing();
      if (!this.destroyed) {
        this.scheduleReconnect();
      }
    };

    this.ws.onerror = () => {
      // onclose fires immediately after onerror; reconnect logic lives there.
    };
  }

  /** Close the connection and suppress further reconnects. */
  disconnect(): void {
    this.destroyed = true;
    this.stopPing();
    if (this.reconnectTimer !== null) {
      clearTimeout(this.reconnectTimer);
      this.reconnectTimer = null;
    }
    this.ws?.close();
    this.ws = null;
    this.connected$.next(false);
  }

  /**
   * Update the local max seq for a channel.
   * Call this after receiving or storing a message so the next ping is accurate.
   */
  updateChannelSeq(channelId: number, seq: number): void {
    const key = String(channelId);
    if ((this.channelSeqs[key] ?? -1) < seq) {
      this.channelSeqs[key] = seq;
    }
  }

  /** Send a typed frame over the WebSocket. Silently drops if not connected. */
  send<T>(type: WSMessageType, payload: T): void {
    if (this.ws?.readyState !== WebSocket.OPEN) return;
    this.ws.send(JSON.stringify({ type, payload }));
  }

  // ---- Lifecycle ----

  ngOnDestroy(): void {
    this.disconnect();
  }

  // ---- Private ----

  private onMessage(raw: string): void {
    let frame: WSFrame;
    try {
      frame = JSON.parse(raw) as WSFrame;
    } catch {
      console.warn('[WS] malformed frame', raw);
      return;
    }

    switch (frame.type) {
      case 'pong': {
        const p = frame.payload as PongPayload;
        this.pong$.next(p);
        break;
      }

      case 'push_msg': {
        const msg = frame.payload as PushMsgPayload;
        // ACK immediately so the server doesn't time out waiting.
        const ack: PushACKPayload = { push_id: msg.push_id };
        this.send<PushACKPayload>('push_ack', ack);
        // Track the latest seq for this channel so pings are accurate.
        this.updateChannelSeq(msg.channel_id, msg.seq);
        this.pushMsg$.next(msg);
        break;
      }

      case 'send_ack': {
        this.sendAck$.next(frame.payload as SendACKPayload);
        break;
      }

      case 'read_sync': {
        this.readSync$.next(frame.payload as ReadSyncPayload);
        break;
      }

      default:
        // Future frame types (sync_resp, etc.) can be handled here.
        break;
    }
  }

  private startPing(): void {
    this.stopPing();
    this.pingTimer = setInterval(() => {
      const payload: PingPayload = { channel_seqs: { ...this.channelSeqs } };
      this.send<PingPayload>('ping', payload);
    }, PING_INTERVAL_MS);
  }

  private stopPing(): void {
    if (this.pingTimer !== null) {
      clearInterval(this.pingTimer);
      this.pingTimer = null;
    }
  }

  /**
   * Schedule a reconnect with exponential backoff.
   * Delay grows from RECONNECT_BASE_DELAY_MS up to 5× that value.
   */
  private scheduleReconnect(): void {
    if (this.reconnectAttempts >= MAX_RECONNECT_ATTEMPTS) {
      console.warn('[WS] max reconnect attempts reached — giving up');
      return;
    }
    const backoffFactor = Math.min(this.reconnectAttempts + 1, 5);
    const delay = RECONNECT_BASE_DELAY_MS * backoffFactor;
    this.reconnectTimer = setTimeout(() => {
      this.reconnectAttempts++;
      this.connect();
    }, delay);
  }

  /** Return the persisted device ID, creating and storing one if needed. */
  private getOrCreateDeviceID(): string {
    let id = localStorage.getItem(DEVICE_ID_KEY);
    if (!id) {
      id = 'web-' + crypto.randomUUID();
      localStorage.setItem(DEVICE_ID_KEY, id);
    }
    return id;
  }
}
