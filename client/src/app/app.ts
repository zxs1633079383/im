import { Component, signal, inject, effect } from '@angular/core';
import { RouterOutlet } from '@angular/router';
import { AuthService } from './core/auth/auth.service';
import { WebSocketService } from './core/ws/websocket.service';

@Component({
  selector: 'app-root',
  imports: [RouterOutlet],
  templateUrl: './app.html',
  styleUrl: './app.scss'
})
export class App {
  protected readonly title = signal('client');

  private auth = inject(AuthService);
  private ws = inject(WebSocketService);

  constructor() {
    // Connect/disconnect WebSocket in sync with authentication state.
    effect(() => {
      if (this.auth.isAuthenticated()) {
        this.ws.connect();
      } else {
        this.ws.disconnect();
      }
    });
  }
}
