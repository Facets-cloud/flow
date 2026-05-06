BINARY      := flow
INSTALL_DIR := $(HOME)/.local/bin

# VERSION is injected into the binary via -ldflags. Defaults to "dev";
# the release workflow overrides this with VERSION=<tag>.
VERSION  ?= dev
LDFLAGS  := -X main.version=$(VERSION)

.PHONY: build install uninstall test clean

build:
	go build -ldflags '$(LDFLAGS)' -o $(BINARY) .

test:
	go test ./...

install: build
	@# Place the binary in $(INSTALL_DIR) so the user's repo dir stays
	@# clean and `rm -rf` of this clone won't break their shell.
	@mkdir -p $(INSTALL_DIR)
	@cp $(BINARY) $(INSTALL_DIR)/$(BINARY)
	@echo "Installed $(BINARY) -> $(INSTALL_DIR)/$(BINARY)"
	@# Offer to add $(INSTALL_DIR) to PATH if it isn't already there.
	@case ":$$PATH:" in \
		*":$(INSTALL_DIR):"*) \
			echo "$(INSTALL_DIR) already in PATH." ;; \
		*) \
			rc_file="$$HOME/.zshrc"; \
			case "$$SHELL" in \
				*/bash) rc_file="$$HOME/.bashrc" ;; \
				*/fish) rc_file="$$HOME/.config/fish/config.fish" ;; \
			esac; \
			line='export PATH="$$HOME/.local/bin:$$PATH"'; \
			echo ""; \
			echo "$(INSTALL_DIR) is NOT in your PATH."; \
			echo "Suggested line for $$rc_file:"; \
			echo "  $$line"; \
			printf "Append it to %s now? [y/N] " "$$rc_file"; \
			read -r reply; \
			case "$$reply" in \
				[yY]|[yY][eE][sS]) \
					if grep -qF "$$line" "$$rc_file" 2>/dev/null; then \
						echo "$$rc_file already contains the line."; \
					else \
						echo "$$line" >> "$$rc_file"; \
						echo "Appended to $$rc_file. Run 'source $$rc_file' or open a new terminal."; \
					fi ;; \
				*) \
					echo "Skipped. Add the line to $$rc_file yourself, or invoke flow with the full path: $(INSTALL_DIR)/$(BINARY)" ;; \
			esac ;; \
	esac
	@# Install skill + SessionStart hook
	@./$(BINARY) skill install --force
	@echo ""
	@echo "Run 'flow init' to create ~/.flow/ and the database."

uninstall:
	@if [ -x "$(INSTALL_DIR)/$(BINARY)" ]; then \
		"$(INSTALL_DIR)/$(BINARY)" skill uninstall; \
		rm -f "$(INSTALL_DIR)/$(BINARY)"; \
		echo "Removed $(INSTALL_DIR)/$(BINARY)."; \
	elif [ -x "./$(BINARY)" ]; then \
		./$(BINARY) skill uninstall; \
	else \
		echo "No installed flow binary found. Skill/hook may already be removed."; \
	fi
	@echo "If you added $(INSTALL_DIR) to your shell rc file, remove that line manually."

clean:
	rm -f $(BINARY) flowde
