// Tiny in-memory "database" so the demo stays self-contained (no external deps).
export interface User {
  id: string;
  name: string;
  email: string;
}

const USERS: Record<string, User> = {
  "1": { id: "1", name: "Ada Lovelace", email: "ada@braid.dev" },
  "2": { id: "2", name: "Alan Turing", email: "alan@braid.dev" },
  "3": { id: "3", name: "Grace Hopper", email: "grace@braid.dev" },
};

export const db = {
  find(id: string): User | undefined {
    return USERS[id];
  },
};
