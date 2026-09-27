-- Assistant conversations (docs/specs/08-assistant.md#surfaces): private
-- Q&A threads (owner_id) and one shared thread per revision (revision_id).

CREATE TABLE assistant_threads (
    id TEXT PRIMARY KEY,
    repo_id TEXT NOT NULL REFERENCES repos (id) ON DELETE CASCADE,
    revision_id TEXT REFERENCES revisions (id) ON DELETE CASCADE,
    owner_id TEXT REFERENCES users (id) ON DELETE CASCADE,
    title TEXT NOT NULL DEFAULT '',
    created_at BIGINT NOT NULL,
    updated_at BIGINT NOT NULL
);

CREATE UNIQUE INDEX assistant_threads_revision ON assistant_threads (revision_id) WHERE revision_id IS NOT NULL;
CREATE INDEX assistant_threads_owner ON assistant_threads (owner_id, repo_id, updated_at);

CREATE TABLE assistant_messages (
    id TEXT PRIMARY KEY,
    thread_id TEXT NOT NULL REFERENCES assistant_threads (id) ON DELETE CASCADE,
    -- user (a person, or tool results) | assistant
    role TEXT NOT NULL,
    author_id TEXT REFERENCES users (id) ON DELETE SET NULL,
    -- provider-neutral blocks (text, tool_use, tool_result) and the context it was asked in
    content TEXT NOT NULL,
    context TEXT NOT NULL DEFAULT '{}',
    run_id TEXT,
    created_at BIGINT NOT NULL
);

CREATE INDEX assistant_messages_thread ON assistant_messages (thread_id, created_at);
