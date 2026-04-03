import { Component, OnInit, inject, signal } from '@angular/core';
import { CommonModule } from '@angular/common';
import { FormsModule } from '@angular/forms';
import { HttpClient } from '@angular/common/http';
import { firstValueFrom } from 'rxjs';

const API_BASE = 'http://localhost:8080/api';

export interface UserSettings {
  user_id: number;
  notification_enabled: boolean;
  theme: string;
  language: string;
}

@Component({
  selector: 'app-settings',
  standalone: true,
  imports: [CommonModule, FormsModule],
  templateUrl: './settings.component.html',
  styleUrls: ['./settings.component.scss'],
})
export class SettingsComponent implements OnInit {
  private http = inject(HttpClient);

  settings = signal<UserSettings | null>(null);
  loading = signal(false);
  saving = signal(false);
  success = signal(false);
  error = signal<string | null>(null);

  readonly themes = [
    { value: 'system', label: 'System Default' },
    { value: 'light', label: 'Light' },
    { value: 'dark', label: 'Dark' },
  ];

  readonly languages = [
    { value: 'en', label: 'English' },
    { value: 'zh', label: '中文' },
    { value: 'ja', label: '日本語' },
    { value: 'ko', label: '한국어' },
  ];

  async ngOnInit(): Promise<void> {
    this.loading.set(true);
    try {
      const s = await firstValueFrom(
        this.http.get<UserSettings>(`${API_BASE}/settings`),
      );
      this.settings.set(s);
    } catch {
      this.error.set('Failed to load settings.');
    } finally {
      this.loading.set(false);
    }
  }

  updateNotification(value: boolean): void {
    this.settings.update(s => s ? { ...s, notification_enabled: value } : s);
  }

  updateTheme(value: string): void {
    this.settings.update(s => s ? { ...s, theme: value } : s);
  }

  updateLanguage(value: string): void {
    this.settings.update(s => s ? { ...s, language: value } : s);
  }

  async save(): Promise<void> {
    const s = this.settings();
    if (!s) return;
    this.saving.set(true);
    this.success.set(false);
    this.error.set(null);
    try {
      const updated = await firstValueFrom(
        this.http.put<UserSettings>(`${API_BASE}/settings`, {
          notification_enabled: s.notification_enabled,
          theme: s.theme,
          language: s.language,
        }),
      );
      this.settings.set(updated);
      this.success.set(true);
    } catch {
      this.error.set('Failed to save settings.');
    } finally {
      this.saving.set(false);
    }
  }
}
