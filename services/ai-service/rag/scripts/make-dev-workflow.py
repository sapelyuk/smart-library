"""Generate the dev workflow: Gemini stays wired, OpenAI added as spare nodes.

The containerized n8n (docker compose up -d n8n) imports this variant on first
start from workflow/import/. It differs from the production export
(book-rag-system.json) only in ways that make local runs frictionless:

- Gemini embeddings and the Gemini chat model stay the WIRED models: they are
  the nodes connected to the vector store, the retrieval tool and the agent.
- OpenAI embeddings and an OpenAI chat model are ADDED as disconnected spare
  nodes (no connections). To move to OpenAI, delete the Gemini wire in the n8n
  UI and connect the OpenAI node in its place; both emit/consume the same
  3072-dim vectors, so the pgvector schema does not change.
- Webhooks use authentication:none so the ai-service container can call them
  without a shared header secret (dev only; production keeps headerAuth).
- The workflow is marked active so the recommend/ingest webhooks register on
  import.

Original workflow (book-rag-system.json) is never modified by this script.
"""
import json
import os

ROOT = os.path.join(os.path.dirname(__file__), "..")
IN_PATH = os.path.join(ROOT, "workflow", "book-rag-system.json")
OUT_PATH = os.path.join(ROOT, "workflow", "book-rag-system-dev.json")

# OpenAI node names. They are kept OUT of the connections block on purpose, so
# n8n imports them as isolated spare nodes.
OPENAI_EMBEDDINGS = "Embeddings OpenAI (spare)"
OPENAI_CHAT = "Chat Model OpenAI (spare)"

# The Gemini nodes this variant keeps wired to the flow.
GEMINI_EMBEDDINGS = "Embeddings Google Gemini"
GEMINI_CHAT = "Chat Model (swap me)"

wf = json.load(open(IN_PATH, encoding="utf-8"))

# 1. Webhook auth -> none (dev). The ai-service container calls the webhook
#    without a header secret.
for n in wf["nodes"]:
    if n["type"] == "n8n-nodes-base.webhook":
        n["parameters"]["authentication"] = "none"
        n["notes"] = "Webhook auth disabled for local dev. In production, re-enable headerAuth."

# 2. Add the OpenAI embeddings node as a disconnected spare.
#    text-embedding-3-large -> 3072 dims, matching vector(3072) in
#    db/01-schema.sql, so it is a drop-in replacement for the Gemini node.
embeddings_pos = next(
    (n["position"] for n in wf["nodes"] if n["name"] == GEMINI_EMBEDDINGS), [80, 1240]
)
wf["nodes"].append(
    {
        "parameters": {
            "modelName": "text-embedding-3-large",
            "options": {"dimensions": 3072},
        },
        "id": "c1000000-0000-4000-8000-000000000001",
        "name": OPENAI_EMBEDDINGS,
        "type": "@n8n/n8n-nodes-langchain.embeddingsOpenAi",
        "typeVersion": 1.2,
        "position": [embeddings_pos[0], embeddings_pos[1] + 200],
        "credentials": {
            "openAiApi": {"id": "REPLACE_ME", "name": "OpenAI (Book RAG spare)"}
        },
        "notes": (
            "SPARE node, intentionally NOT connected. To switch from Gemini: "
            "disconnect '"
            + GEMINI_EMBEDDINGS
            + "' from the vector store and the retrieval tool, then connect "
            "this node instead. text-embedding-3-large emits 3072 dims, the "
            "same as vector(3072) in db/01-schema.sql."
        ),
    }
)

# 3. Add the OpenAI chat model as a disconnected spare.
chat_pos = next(
    (n["position"] for n in wf["nodes"] if n["name"] == GEMINI_CHAT), [-820, 2660]
)
wf["nodes"].append(
    {
        "parameters": {
            "modelName": "gpt-4o-mini",
            "options": {"temperature": 0.3, "maxOutputTokens": 2048},
        },
        "id": "c1000000-0000-4000-8000-000000000002",
        "name": OPENAI_CHAT,
        "type": "@n8n/n8n-nodes-langchain.lmChatOpenAi",
        "typeVersion": 1.2,
        "position": [chat_pos[0], chat_pos[1] + 200],
        "credentials": {
            "openAiApi": {"id": "REPLACE_ME", "name": "OpenAI (Book RAG spare)"}
        },
        "notes": (
            "SPARE node, intentionally NOT connected. To switch from Gemini: "
            "disconnect '"
            + GEMINI_CHAT
            + "' from the agent, then connect this node to the agent's "
            "language-model slot instead."
        ),
    }
)

# 4. Mark the workflow active so the webhooks register on import.
wf["active"] = True

# 5. Rename for clarity.
wf["name"] = "Book RAG System (dev)"

with open(OUT_PATH, "w", encoding="utf-8") as f:
    json.dump(wf, f, indent=2, ensure_ascii=False)

# 6. Publish a copy into the directory the n8n container imports.
import_dir = os.path.join(ROOT, "workflow", "import")
os.makedirs(import_dir, exist_ok=True)
import_path = os.path.join(import_dir, "book-rag-system-dev.json")
with open(import_path, "w", encoding="utf-8") as f:
    json.dump(wf, f, indent=2, ensure_ascii=False)

print(f"Dev workflow written to {OUT_PATH}")
print(f"Import copy written to {import_path}")
print("  - Gemini embeddings + chat stay WIRED")
print("  - OpenAI embeddings + chat added as DISCONNECTED spares")
print("  - Webhook auth -> none (dev-only)")
print("  - Active -> true")
