"""Generate a dev-friendly workflow: OpenAI models + webhook auth disabled + active.

This variant is meant for local development where:
- n8n runs in Docker (docker compose up -d n8n)
- Webhooks are called from the ai-service container on the compose network
- Credentials (OpenAI API, Postgres) are attached in the n8n UI

Original workflow (book-rag-system.json) uses Gemini and header auth.
This variant swaps Gemini for OpenAI-compatible models and disables
webhook header auth so the AI service can call the webhook without
credential friction (dev-only).
"""
import json
import os

ROOT = os.path.join(os.path.dirname(__file__), "..")
IN_PATH = os.path.join(ROOT, "workflow", "book-rag-system.json")
OUT_PATH = os.path.join(ROOT, "workflow", "book-rag-system-dev.json")

wf = json.load(open(IN_PATH))

# 1. Swap Gemini embeddings → OpenAI
for n in wf["nodes"]:
    if n["type"] == "@n8n/n8n-nodes-langchain.embeddingsGoogleGemini":
        n["type"] = "@n8n/n8n-nodes-langchain.embeddingsOpenAi"
        n["name"] = "Embeddings OpenAI"
        n["parameters"] = {
            "modelName": "text-embedding-3-large",
            "options": {"dimensions": 3072},
        }
        n["credentials"] = {
            "openAiApi": {"id": "REPLACE_ME", "name": "OpenAI Compatible API"}
        }
        n["notes"] = (
            "Swapped from Gemini. text-embedding-3-large → 3072 dims "
            "— matches vector(3072) in db/01-schema.sql. "
            "Attach an 'OpenAI API' credential in the n8n UI."
        )

# 2. Swap Gemini chat model → OpenAI
for n in wf["nodes"]:
    if n["type"] == "@n8n/n8n-nodes-langchain.lmChatGoogleGemini":
        n["type"] = "@n8n/n8n-nodes-langchain.lmChatOpenAi"
        n["name"] = "Chat Model (OpenAI)"
        n["parameters"] = {
            "modelName": "gpt-4o-mini",
            "options": {"temperature": 0.3, "maxOutputTokens": 2048},
        }
        n["credentials"] = {
            "openAiApi": {"id": "REPLACE_ME", "name": "OpenAI Compatible API"}
        }
        n["notes"] = (
            "Swapped from Gemini to OpenAI-compatible. "
            "Attach an 'OpenAI API' credential in the n8n UI."
        )

# 2b. Rename connection keys to match the renamed nodes.
# The connections block references nodes by NAME, so renaming a node without
# renaming its connection keys breaks the import validation.
RENAME = {
    "Embeddings Google Gemini": "Embeddings OpenAI",
    "Chat Model (swap me)": "Chat Model (OpenAI)",
}

connections = wf.get("connections", {})

# Keys of the connections dict are source node names.
for old, new in RENAME.items():
    if old in connections:
        connections[new] = connections.pop(old)

# Connection targets reference node names inside main/ai_languageModel links.
for source, groups in connections.items():
    for conn_type, rows in groups.items():
        for row in rows:
            if not isinstance(row, list):
                continue
            for link in row:
                if isinstance(link, dict) and link.get("node") in RENAME:
                    link["node"] = RENAME[link["node"]]

# 3. Disable webhook header auth (dev-friendly)
for n in wf["nodes"]:
    if n["type"] == "n8n-nodes-base.webhook":
        n["parameters"]["authentication"] = "none"
        n["notes"] = "Webhook auth disabled for local dev. In production, re-enable headerAuth."

# 4. Mark workflow as active (import:workflow will activate it)
wf["active"] = True

# 5. Rename for clarity
wf["name"] = "Book RAG System (dev)"

out = OUT_PATH
with open(out, "w", encoding="utf-8") as f:
    json.dump(wf, f, indent=2, ensure_ascii=False)
print(f"Dev workflow written to {out}")
print("  - Gemini -> OpenAI embeddings + chat")
print("  - Webhook auth -> none (dev-only)")
print("  - Active -> true")
