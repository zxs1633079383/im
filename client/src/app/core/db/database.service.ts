import { Injectable } from '@angular/core';
import { CREATE_TABLES_SQL, SCHEMA_VERSION } from './schema';

@Injectable({ providedIn: 'root' })
export class DatabaseService {
  private db: any = null;
  private _available = false;

  /** Whether SQLite is available (Tauri environment). */
  get available(): boolean {
    return this._available;
  }

  async initialize(): Promise<void> {
    try {
      const { default: Database } = await import('@tauri-apps/plugin-sql');
      this.db = await Database.load('sqlite:im.db');
      await this.db.execute(CREATE_TABLES_SQL);
      await this.db.execute(
        `INSERT OR IGNORE INTO local_meta (key, value) VALUES ('schema_version', $1)`,
        [String(SCHEMA_VERSION)]
      );
      this._available = true;
      console.info('[DatabaseService] SQLite initialized');
    } catch (err) {
      this._available = false;
      this.db = null;
      console.warn('[DatabaseService] SQLite unavailable (browser mode), using memory only', err);
    }
  }

  async execute(sql: string, params: unknown[] = []): Promise<void> {
    if (!this._available) return;
    await this.db!.execute(sql, params);
  }

  async query<T>(sql: string, params: unknown[] = []): Promise<T[]> {
    if (!this._available) return [];
    return await this.db!.select(sql, params) as T[];
  }

  async getOne<T>(sql: string, params: unknown[] = []): Promise<T | null> {
    const rows = await this.query<T>(sql, params);
    return rows.length > 0 ? rows[0] : null;
  }
}
