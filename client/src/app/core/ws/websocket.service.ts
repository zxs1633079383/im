import { Injectable, OnDestroy, inject } from '@angular/core';
import { Subject, BehaviorSubject } from 'rxjs';
import { AuthService } from '../auth/auth.service';
import { DatabaseService } from '../db/database.service';
import {
  WSFrame, WSMessageType,
  PingPayload, PongPayload,
  PushMsgPayload, PushACKPayload,
  SendACKPayload, SendPayload,
  ReadSyncPayload,
  FriendEventPayload,
  ChannelEventPayload,
} from './websocket.models';
import { WS_BASE } from '../config/api.config';

const WS_URL = WS_BASE;
const PING_INTERVAL_MS = 15_000;
const RECONNECT_BASE_DELAY_MS = 3_000;
const MAX_RECONNECT_ATTEMPTS = 10;
const DEVICE_ID_KEY = 'im_device_id';

@Injectable({ providedIn: 'root' })
export class WebSocketService implements OnDestroy {
  private auth = inject(AuthService);
  private db = inject(DatabaseService);

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

  /** Emits when a friend event (request, accept, reject) arrives. */
  readonly friendEvent$ = new Subject<FriendEventPayload>();

  /** Emits when a channel event (e.g. added to a group) arrives. */
  readonly channelEvent$ = new Subject<ChannelEventPayload>();

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

    // Load persisted channel seqs before connecting (best-effort, non-blocking)
    this.loadChannelSeqsFromDb().catch(() => {});

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
      // Persist to SQLite (best-effort, fire-and-forget)
      this.persistChannelSeq(channelId, seq);
    }
  }

  /**
   * Load channel seqs from SQLite into the in-memory map.
   * Should be called before connecting on app start or reconnect.
   */
  async loadChannelSeqsFromDb(): Promise<void> {
    if (!this.db.available) return;
    try {
      interface Row { id: string; server_seq: number }
      const rows = await this.db.query<Row>(
        `SELECT id, server_seq FROM local_channels WHERE server_seq > 0`
      );
      for (const row of rows) {
        const key = row.id;
        const current = this.channelSeqs[key] ?? -1;
        if (row.server_seq > current) {
          this.channelSeqs[key] = row.server_seq;
        }
      }
    } catch (err) {
      console.warn('[WebSocketService] loadChannelSeqsFromDb failed', err);
    }
  }

  /** Persist a channel seq to local_channels. */
  private persistChannelSeq(channelId: number, seq: number): void {
    if (!this.db.available) return;
    this.db.execute(
      `INSERT INTO local_channels (id, type, name, server_seq)
       VALUES ($1, 0, '', $2)
       ON CONFLICT(id) DO UPDATE SET server_seq = MAX(local_channels.server_seq, $2)`,
      [String(channelId), seq]
    ).catch(err => console.warn('[WebSocketService] persistChannelSeq failed', err));
  }

  /** Send a typed frame over the WebSocket. Silently drops if not connected. */
  send<T>(type: WSMessageType, payload: T): void {
    if (this.ws?.readyState !== WebSocket.OPEN) return;
    this.ws.send(JSON.stringify({ type, payload }));
  }

  /**
   * Send a chat message via WebSocket (faster than HTTP).
   * Returns true if the frame was sent; false if WS is not open.
   */
  sendViaWs(channelId: number, content: string, clientMsgId: string, msgType = 1): boolean {
    if (this.ws?.readyState !== WebSocket.OPEN) return false;
    const payload: SendPayload = {
      client_msg_id: clientMsgId,
      channel_id: channelId,
      content,
      msg_type: msgType,
    };
    this.send('send', payload);
    return true;
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

      case 'friend_event': {
        this.friendEvent$.next(frame.payload as FriendEventPayload);
        break;
      }

      case 'channel_event': {
        this.channelEvent$.next(frame.payload as ChannelEventPayload);
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
