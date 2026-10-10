import { Hono } from "hono";
import { getUser } from "./users.js";

const app = new Hono();

app.get("/", (c) => c.text("Braid demo API"));

app.get("/users/:id", (c) => {
  const id = c.req.param("id");
  const user = getUser(id);
  if (!user) return c.json({ error: "not found" }, 404);
  return c.json(user);
});

export default app;
