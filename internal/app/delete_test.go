package app

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	"flow/internal/flowdb"
)

func TestCmdDeleteAndRestoreTask(t *testing.T) {
	root, db := showListEditDB(t)
	insertTask(t, db, "old-task", "Old Task", "backlog", "medium", filepath.Join(root, "x"), nil)

	if rc := cmdDelete([]string{"old-task"}); rc != 0 {
		t.Fatalf("delete rc=%d", rc)
	}
	var deletedAt sql.NullString
	if err := db.QueryRow(`SELECT deleted_at FROM tasks WHERE slug = ?`, "old-task").Scan(&deletedAt); err != nil {
		t.Fatal(err)
	}
	if !deletedAt.Valid || deletedAt.String == "" {
		t.Fatalf("deleted_at not set: %+v", deletedAt)
	}

	tasks, err := flowdb.ListTasks(db, flowdb.TaskFilter{Kind: ""})
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 0 {
		t.Fatalf("deleted task leaked into default list: %+v", tasks)
	}
	tasks, err = flowdb.ListTasks(db, flowdb.TaskFilter{Kind: "", DeletedOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 || tasks[0].Slug != "old-task" {
		t.Fatalf("deleted-only list = %+v", tasks)
	}

	out := captureStdout(t, func() {
		if rc := cmdList([]string{"tasks", "--deleted"}); rc != 0 {
			t.Fatalf("list --deleted rc=%d", rc)
		}
	})
	if !strings.Contains(out, "old-task") || !strings.Contains(out, "(deleted)") {
		t.Fatalf("deleted list missing marker: %q", out)
	}

	out = captureStdout(t, func() {
		if rc := cmdShow([]string{"task", "old-task"}); rc != 0 {
			t.Fatalf("show deleted task rc=%d", rc)
		}
	})
	if !strings.Contains(out, "(deleted)") || !strings.Contains(out, "deleted:") {
		t.Fatalf("show deleted task missing metadata: %q", out)
	}

	if rc := cmdRestore([]string{"old-task"}); rc != 0 {
		t.Fatalf("restore rc=%d", rc)
	}
	if err := db.QueryRow(`SELECT deleted_at FROM tasks WHERE slug = ?`, "old-task").Scan(&deletedAt); err != nil {
		t.Fatal(err)
	}
	if deletedAt.Valid {
		t.Fatalf("deleted_at still set after restore: %+v", deletedAt)
	}
	tasks, err = flowdb.ListTasks(db, flowdb.TaskFilter{Kind: ""})
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 || tasks[0].Slug != "old-task" {
		t.Fatalf("restored task missing from default list: %+v", tasks)
	}
}

func TestCmdDeleteProjectAndPlaybook(t *testing.T) {
	root, db := showListEditDB(t)
	insertProject(t, db, "old-project", "Old Project", filepath.Join(root, "old"), "low")
	if err := flowdb.UpsertPlaybook(db, &flowdb.Playbook{
		Slug:    "old-playbook",
		Name:    "Old Playbook",
		WorkDir: filepath.Join(root, "old"),
	}); err != nil {
		t.Fatal(err)
	}

	if rc := cmdDelete([]string{"project/old-project"}); rc != 0 {
		t.Fatalf("delete project rc=%d", rc)
	}
	if rc := cmdDelete([]string{"playbook/old-playbook"}); rc != 0 {
		t.Fatalf("delete playbook rc=%d", rc)
	}

	projects, err := flowdb.ListProjects(db, flowdb.ProjectFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(projects) != 0 {
		t.Fatalf("deleted project leaked into default list: %+v", projects)
	}
	playbooks, err := flowdb.ListPlaybooks(db, flowdb.PlaybookFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(playbooks) != 0 {
		t.Fatalf("deleted playbook leaked into default list: %+v", playbooks)
	}

	out := captureStdout(t, func() {
		if rc := cmdList([]string{"projects", "--deleted"}); rc != 0 {
			t.Fatalf("list projects --deleted rc=%d", rc)
		}
	})
	if !strings.Contains(out, "old-project") || !strings.Contains(out, "(deleted)") {
		t.Fatalf("deleted project missing from list: %q", out)
	}
	out = captureStdout(t, func() {
		if rc := cmdList([]string{"playbooks", "--deleted"}); rc != 0 {
			t.Fatalf("list playbooks --deleted rc=%d", rc)
		}
	})
	if !strings.Contains(out, "old-playbook") || !strings.Contains(out, "(deleted)") {
		t.Fatalf("deleted playbook missing from list: %q", out)
	}

	if rc := cmdRestore([]string{"project/old-project"}); rc != 0 {
		t.Fatalf("restore project rc=%d", rc)
	}
	if rc := cmdRestore([]string{"playbook/old-playbook"}); rc != 0 {
		t.Fatalf("restore playbook rc=%d", rc)
	}
	projects, _ = flowdb.ListProjects(db, flowdb.ProjectFilter{})
	playbooks, _ = flowdb.ListPlaybooks(db, flowdb.PlaybookFilter{})
	if len(projects) != 1 || projects[0].Slug != "old-project" {
		t.Fatalf("restored project missing: %+v", projects)
	}
	if len(playbooks) != 1 || playbooks[0].Slug != "old-playbook" {
		t.Fatalf("restored playbook missing: %+v", playbooks)
	}
}

func TestCmdRestoreKeepsArchivedState(t *testing.T) {
	root, db := showListEditDB(t)
	insertTask(t, db, "archived-task", "Archived Task", "backlog", "medium", filepath.Join(root, "x"), nil)
	now := flowdb.NowISO()
	if _, err := db.Exec(`UPDATE tasks SET archived_at = ?, deleted_at = ? WHERE slug = ?`, now, now, "archived-task"); err != nil {
		t.Fatal(err)
	}

	if rc := cmdRestore([]string{"archived-task"}); rc != 0 {
		t.Fatalf("restore rc=%d", rc)
	}
	var archivedAt, deletedAt sql.NullString
	if err := db.QueryRow(`SELECT archived_at, deleted_at FROM tasks WHERE slug = ?`, "archived-task").Scan(&archivedAt, &deletedAt); err != nil {
		t.Fatal(err)
	}
	if !archivedAt.Valid {
		t.Fatalf("restore should not unarchive the task")
	}
	if deletedAt.Valid {
		t.Fatalf("restore should clear deleted_at: %+v", deletedAt)
	}
}

func TestCmdDeleteAmbiguousSlugRequiresPrefix(t *testing.T) {
	root, db := showListEditDB(t)
	insertProject(t, db, "shared", "Shared Project", filepath.Join(root, "p"), "medium")
	insertTask(t, db, "shared", "Shared Task", "backlog", "medium", filepath.Join(root, "t"), nil)

	if rc := cmdDelete([]string{"shared"}); rc == 0 {
		t.Fatalf("unprefixed delete should reject slug shared across kinds")
	}
	if rc := cmdDelete([]string{"task/shared"}); rc != 0 {
		t.Fatalf("prefixed task delete rc=%d", rc)
	}
	task, err := flowdb.GetTask(db, "shared")
	if err != nil {
		t.Fatal(err)
	}
	project, err := flowdb.GetProject(db, "shared")
	if err != nil {
		t.Fatal(err)
	}
	if !task.DeletedAt.Valid {
		t.Fatalf("task was not deleted")
	}
	if project.DeletedAt.Valid {
		t.Fatalf("project was unexpectedly deleted")
	}
}
