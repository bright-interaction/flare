import { api } from './api';
import type { User } from './types';

export const session = $state<{ user: User | null; loaded: boolean }>({
  user: null,
  loaded: false
});

export async function loadSession(): Promise<void> {
  try {
    session.user = await api.me();
  } catch {
    session.user = null;
  }
  session.loaded = true;
}
