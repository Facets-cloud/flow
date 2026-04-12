BINARY   := flow
REPO_DIR := $(shell pwd)

.PHONY: build install uninstall test clean

build:
	go build -o $(BINARY) .

test:
	go test ./...

install: build
	@# Ensure repo dir is in PATH via ~/.zshrc
	@if ! grep -qF '$(REPO_DIR)' ~/.zshrc 2>/dev/null; then \
		echo 'export PATH="$(REPO_DIR):$$PATH"' >> ~/.zshrc; \
		echo "Added $(REPO_DIR) to PATH in ~/.zshrc"; \
	else \
		echo "$(REPO_DIR) already in ~/.zshrc PATH"; \
	fi
	@# Initialize data dir + install skill + hook
	./$(BINARY) init
	@echo ""
	@echo "Done. Run 'source ~/.zshrc' or open a new terminal to use flow."

uninstall:
	./$(BINARY) skill uninstall
	@echo "Skill and hook removed. Remove the PATH line from ~/.zshrc manually if desired."

clean:
	rm -f $(BINARY)
