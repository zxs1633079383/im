import { Component, OnInit, inject, signal, computed } from '@angular/core';
import { CommonModule } from '@angular/common';
import { FormsModule } from '@angular/forms';
import { HttpClient } from '@angular/common/http';
import { firstValueFrom } from 'rxjs';
import { ThemeService, Theme } from '../../core/theme/theme.service';
import { I18nService } from '../../core/i18n/i18n.service';
import { API_BASE } from '../../core/config/api.config';

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
  private themeService = inject(ThemeService);
  private i18n = inject(I18nService);

  settings = signal<UserSettings | null>(null);
  loading = signal(false);

  // These reflect the ACTUAL active theme/locale, so select boxes always match reality
  currentTheme = computed(() => this.themeService.theme());
  currentLanguage = computed(() => this.i18n.locale());
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

  t(key: string): string {
    return this.i18n.t(key);
  }

  async ngOnInit(): Promise<void> {
    this.loading.set(true);
    try {
      const s = await firstValueFrom(
        this.http.get<UserSettings>(`${API_BASE}/settings`),
      );
      this.settings.set(s);
      // Sync loaded settings to theme/i18n services so UI matches
      this.themeService.applyTheme(s.theme as any);
      this.i18n.setLocale(s.language);
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
    this.themeService.applyTheme(value as Theme);  // Instant preview
  }

  updateLanguage(value: string): void {
    this.settings.update(s => s ? { ...s, language: value } : s);
    this.i18n.setLocale(value);  // Instant switch
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
      // Apply theme and locale immediately
      this.themeService.applyTheme(updated.theme as Theme);
      this.i18n.setLocale(updated.language);
      this.success.set(true);
    } catch {
      this.error.set('Failed to save settings.');
    } finally {
      this.saving.set(false);
    }
  }
}
