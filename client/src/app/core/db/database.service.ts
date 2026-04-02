import { Injectable } from '@angular/core';
import Database from '@tauri-apps/plugin-sql';
import { CREATE_TABLES_SQL, SCHEMA_VERSION } from './schema';

@Injectable({ providedIn: 'root' })
export class DatabaseService {
  private db: Database | null = null;

  async initialize(): Promise<void> {
    this.db = await Database.load('sqlite:im.db');
    await this.db.execute(CREATE_TABLES_SQL);
    await this.db.execute(
      `INSERT OR IGNORE INTO local_meta (key, value) VALUES ('schema_version', $1)`,
      [String(SCHEMA_VERSION)]
    );
  }

  async execute(sql: string, params: unknown[] = []): Promise<void> {
    this.ensureInitialized();
    await this.db!.execute(sql, params);
  }

  async query<T>(sql: string, params: unknown[] = []): Promise<T[]> {
    this.ensureInitialized();
    return await this.db!.select<T[]>(sql, params);
  }

  async getOne<T>(sql: string, params: unknown[] = []): Promise<T | null> {
    const rows = await this.query<T>(sql, params);
    return rows.length > 0 ? rows[0] : null;
  }

  private ensureInitialized(): void {
    if (!this.db) {
      throw new Error('Database not initialized. Call initialize() first.');
    }
  }
}
