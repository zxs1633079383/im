import {
  Component,
  inject,
  OnInit,
  OnDestroy,
  signal,
  computed,
  ViewChild,
  ElementRef,
  AfterViewChecked,
  DestroyRef,
} from '@angular/core';
import { CommonModule } from '@angular/common';
import { FormsModule } from '@angular/forms';
import { ActivatedRoute, Router } from '@angular/router';
import { takeUntilDestroyed } from '@angular/core/rxjs-interop';
import { MessageService, Message } from '../../core/messages/message.service';
import { AuthService } from '../../core/auth/auth.service';
import { ChannelService, ChannelWithPreview } from '../../core/channels/channel.service';

export interface MessageGroup {
  senderId: number;
  senderName: string;
  isMine: boolean;
  messages: Message[];
  /** Localized date string — only set when a date separator should appear above this group */
  dateSeparator: string | null;
  isSystem: boolean;  // true when the message is msg_type === 4
}

@Component({
  selector: 'app-chat',
  standalone: true,
  imports: [CommonModule, FormsModule],
  templateUrl: './chat.component.html',
  styleUrl: './chat.component.scss',
})
export class ChatComponent implements OnInit, OnDestroy, AfterViewChecked {
  private route = inject(ActivatedRoute);
  private messageService = inject(MessageService);
  private auth = inject(AuthService);
  private channelService = inject(ChannelService);
  private router = inject(Router);
  private destroyRef = inject(DestroyRef);

  @ViewChild('messageList') private messageListRef!: ElementRef<HTMLElement>;

  messageText = signal('');
  sending = signal(false);
  error = signal<string | null>(null);

  /** The message the user is currently replying to. Cleared after send. */
  readonly replyTarget = signal<Message | null>(null);

  /** Context menu state */
  readonly contextMenuMsg = signal<Message | null>(null);
  readonly contextMenuPos = signal<{ x: number; y: number } | null>(null);

  /** True when the user has scrolled up and is not at the bottom. */
  readonly isScrolledUp = signal(false);

  /** Visible messages: filter out phantom messages (msg_type === 99). */
  readonly visibleMessages = computed(() =>
    this.messageService.messages().filter(m => m.msg_type !== 99)
  );

  /** Messages grouped by consecutive sender, with date separators and system message support. */
  readonly groupedMessages = computed<MessageGroup[]>(() => {
    const msgs = this.visibleMessages();
    const me = this.auth.currentUser();
    const groups: MessageGroup[] = [];
    let lastDate = '';

    for (const msg of msgs) {
      const msgDate = new Date(msg.created_at).toLocaleDateString();
      const dateSeparator = msgDate !== lastDate ? msgDate : null;
      if (dateSeparator) lastDate = msgDate;

      const isSystem = msg.msg_type === 4;
      const isMine = msg.sender_id === (me?.id ?? -1);
      const senderName = this.messageService.getSenderName(msg.sender_id);

      // Merge into last group if same sender, not system, within 5 minutes, no date separator
      const last = groups[groups.length - 1];
      const canMerge =
        last &&
        !isSystem &&
        !last.isSystem &&
        last.senderId === msg.sender_id &&
        dateSeparator === null &&
        new Date(msg.created_at).getTime() -
          new Date(last.messages[last.messages.length - 1].created_at).getTime() <
          5 * 60 * 1000;

      if (canMerge) {
        last.messages.push(msg);
      } else {
        groups.push({
          senderId: msg.sender_id,
          senderName,
          isMine,
          messages: [msg],
          dateSeparator,
          isSystem,
        });
      }
    }

    return groups;
  });

  /** The ChannelWithPreview for the currently-open channel. */
  readonly activeChannel = computed<ChannelWithPreview | undefined>(() =>
    this.channelService.channels().find(ch => ch.id === this.channelId)
  );

  readonly channelName = computed(() => {
    const ch = this.activeChannel();
    if (!ch) return '';
    return this.channelService.channelLabel(ch);
  });

  readonly memberCount = computed(() =>
    this.channelService.memberCounts()[this.channelId] ?? null
  );

  private channelId = 0;
  private shouldScrollToBottom = false;
  isLoadingOlder = false;

  ngOnInit(): void {
    this.route.paramMap.subscribe(params => {
      const id = Number(params.get('id'));
      if (id && id !== this.channelId) {
        this.channelId = id;
        this.replyTarget.set(null);
        this.closeContextMenu();
        this.loadChannel(id);
      }
    });

    // When a new message arrives and we're not scrolled up, auto-scroll to bottom
    this.messageService._newMessageArrived
      .pipe(takeUntilDestroyed(this.destroyRef))
      .subscribe(() => {
        if (!this.isScrolledUp()) {
          this.shouldScrollToBottom = true;
        }
      });
  }

  ngOnDestroy(): void {
    this.messageService.clear();
  }

  ngAfterViewChecked(): void {
    if (this.shouldScrollToBottom) {
      this.scrollToBottom();
      this.shouldScrollToBottom = false;
    }
  }

  private async loadChannel(channelId: number): Promise<void> {
    this.error.set(null);
    try {
      await Promise.all([
        this.messageService.loadMessages(channelId),
        this.channelService.loadMemberCount(channelId),
      ]);
      this.shouldScrollToBottom = true;
    } catch (err) {
      this.error.set('Failed to load messages.');
      console.error(err);
    }
  }

  openSettings(): void {
    this.router.navigate(['channels', this.channelId, 'settings']);
  }

  private scrollToBottom(): void {
    if (this.messageListRef?.nativeElement) {
      const el = this.messageListRef.nativeElement;
      el.scrollTop = el.scrollHeight;
    }
  }

  onInput(event: Event): void {
    this.messageText.set((event.target as HTMLTextAreaElement).value);
  }

  onKeyDown(event: KeyboardEvent): void {
    if (event.key === 'Enter' && !event.shiftKey) {
      event.preventDefault();
      this.send();
    }
  }

  async send(): Promise<void> {
    const content = this.messageText().trim();
    if (!content || this.sending() || !this.channelId) {
      return;
    }

    const clientMsgId = crypto.randomUUID();
    const currentUser = this.auth.currentUser();
    const replyTo = this.replyTarget()?.seq ?? undefined;

    // Optimistic message
    const optimistic: Message = {
      id: -1,
      channel_id: this.channelId,
      seq: -1,
      client_msg_id: clientMsgId,
      sender_id: currentUser?.id ?? 0,
      msg_type: 1,
      content,
      reply_to: replyTo,
      created_at: new Date().toISOString(),
    };

    this.messageService.appendOptimistic(optimistic);
    this.messageText.set('');
    this.replyTarget.set(null);   // clear reply after send
    this.shouldScrollToBottom = true;
    this.sending.set(true);
    this.error.set(null);

    try {
      const confirmed = await this.messageService.sendMessage(this.channelId, {
        content,
        client_msg_id: clientMsgId,
        msg_type: 1,
        reply_to: replyTo,
      });
      this.messageService.confirmSent(clientMsgId, confirmed);
      this.shouldScrollToBottom = true;
    } catch (err) {
      this.error.set('Failed to send message.');
      console.error(err);
      // Remove the optimistic message on failure
      this.messageService.removeOptimistic(clientMsgId);
    } finally {
      this.sending.set(false);
    }
  }

  /** The seq of the oldest message currently displayed (skipping optimistic ones). */
  get oldestSeq(): number {
    const msgs = this.messageService.messages().filter(m => m.seq > 0);
    return msgs.length > 0 ? msgs[0].seq : 0;
  }

  /**
   * Scroll event handler on the message list container.
   * Tracks whether the user is scrolled up, and triggers hole detection at the top.
   */
  onScroll(event: Event): void {
    const el = event.target as HTMLElement;
    const distanceFromBottom = el.scrollHeight - el.scrollTop - el.clientHeight;
    this.isScrolledUp.set(distanceFromBottom > 80);
    if (el.scrollTop === 0) {
      this.onScrolledToTop();
    }
  }

  /**
   * Called when the message list scroll container reaches the top.
   * Triggers count-based hole detection and prepends older messages.
   */
  async onScrolledToTop(): Promise<void> {
    const channelId = this.messageService.activeChannelId();
    if (!channelId || this.isLoadingOlder) return;

    const pivot = this.oldestSeq;
    if (pivot <= 1) return; // already at the beginning

    this.isLoadingOlder = true;
    try {
      const older = await this.messageService.detectAndFillHole(channelId, pivot);
      if (older.length > 0) {
        this.messageService.messages.update(current => {
          const existingSeqs = new Set(current.map(m => m.seq));
          const newOnes = older.filter(m => !existingSeqs.has(m.seq));
          return [...newOnes, ...current];
        });
      }
    } finally {
      this.isLoadingOlder = false;
    }
  }

  isMine(msg: Message): boolean {
    return msg.sender_id === this.auth.currentUser()?.id;
  }

  formatTime(dateStr: string): string {
    const d = new Date(dateStr);
    return d.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' });
  }

  isOptimistic(msg: Message): boolean {
    return msg.id === -1;
  }

  // ---- Context menu ----

  onContextMenu(event: MouseEvent, msg: Message): void {
    event.preventDefault();
    this.contextMenuMsg.set(msg);
    this.contextMenuPos.set({ x: event.clientX, y: event.clientY });
  }

  closeContextMenu(): void {
    this.contextMenuMsg.set(null);
    this.contextMenuPos.set(null);
  }

  replyToMessage(msg: Message): void {
    this.replyTarget.set(msg);
    this.closeContextMenu();
    // Focus the textarea
    setTimeout(() => {
      const ta = document.querySelector<HTMLTextAreaElement>('.message-input');
      ta?.focus();
    }, 0);
  }

  copyMessageText(msg: Message): void {
    navigator.clipboard.writeText(msg.content).catch(() => {});
    this.closeContextMenu();
  }

  cancelReply(): void {
    this.replyTarget.set(null);
  }

  // ---- Reply preview helpers ----

  /** Find the message being replied to for display in bubble */
  getReplyTargetMsg(replySeq: number): Message | undefined {
    return this.messageService.messages().find(m => m.seq === replySeq);
  }

  getReplyTargetPreview(replySeq: number): string {
    const msg = this.getReplyTargetMsg(replySeq);
    if (!msg) return `Message #${replySeq}`;
    const name = this.messageService.getSenderName(msg.sender_id);
    const preview = msg.content.length > 60 ? msg.content.slice(0, 60) + '\u2026' : msg.content;
    return `${name}: ${preview}`;
  }
}
