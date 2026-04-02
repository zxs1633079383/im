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
} from '@angular/core';
import { CommonModule } from '@angular/common';
import { FormsModule } from '@angular/forms';
import { ActivatedRoute } from '@angular/router';
import { MessageService, Message } from '../../core/messages/message.service';
import { AuthService } from '../../core/auth/auth.service';

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

  @ViewChild('messageList') private messageListRef!: ElementRef<HTMLElement>;

  messageText = signal('');
  sending = signal(false);
  error = signal<string | null>(null);

  /** Visible messages: filter out phantom messages (msg_type === 99). */
  readonly visibleMessages = computed(() =>
    this.messageService.messages().filter(m => m.msg_type !== 99)
  );

  private channelId = 0;
  private shouldScrollToBottom = false;

  ngOnInit(): void {
    this.route.paramMap.subscribe(params => {
      const id = Number(params.get('id'));
      if (id && id !== this.channelId) {
        this.channelId = id;
        this.loadChannel(id);
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
      await this.messageService.loadMessages(channelId);
      this.shouldScrollToBottom = true;
    } catch (err) {
      this.error.set('Failed to load messages.');
      console.error(err);
    }
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

    // Optimistic message
    const optimistic: Message = {
      id: -1,
      channel_id: this.channelId,
      seq: -1,
      client_msg_id: clientMsgId,
      sender_id: currentUser?.id ?? 0,
      msg_type: 1,
      content,
      created_at: new Date().toISOString(),
    };

    this.messageService.appendOptimistic(optimistic);
    this.messageText.set('');
    this.shouldScrollToBottom = true;
    this.sending.set(true);
    this.error.set(null);

    try {
      const confirmed = await this.messageService.sendMessage(this.channelId, {
        content,
        client_msg_id: clientMsgId,
        msg_type: 1,
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
}
