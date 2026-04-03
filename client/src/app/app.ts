import { Component, signal, inject, effect } from '@angular/core';
import { RouterOutlet } from '@angular/router';
import { AuthService } from './core/auth/auth.service';
import { WebSocketService } from './core/ws/websocket.service';
import { ThemeService, Theme } from './core/theme/theme.service';
import { I18nService } from './core/i18n/i18n.service';

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
  private themeService = inject(ThemeService);
  private i18nService = inject(I18nService);

  constructor() {
    // Apply persisted theme on startup
    this.themeService.applyTheme(this.themeService.theme() as Theme);

    // Apply persisted locale on startup (no-op beyond reading signal, already stored)
    // locale signal is initialized from localStorage in I18nService constructor

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
