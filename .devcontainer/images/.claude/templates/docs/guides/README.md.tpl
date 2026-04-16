# Guides

Developer and user guides for **{{PROJECT_NAME}}**.

## Developer Guides

### Getting Started

1. **Clone the repository**
   ```bash
   git clone {{REPO_URL}}
   cd {{PROJECT_NAME}}
   ```

2. **Start the development environment**
   ```bash
   # If using DevContainer
   code .
   # Then: Ctrl+Shift+P → "Dev Containers: Reopen in Container"
   ```

3. **Install dependencies**
   ```bash
   # (Add your install command here)
   ```

4. **Run the application**
   ```bash
   # (Add your run command here)
   ```

### Project Structure

```
{{PROJECT_NAME}}/
├── src/                # Source code
│   ├── components/     # UI components
│   ├── services/       # Business logic
│   └── utils/          # Utilities
├── tests/              # Test files
├── docs/               # Additional documentation
└── .docs/              # MkDocs site (this documentation)
```

### Development Workflow

1. Create a feature branch: `git checkout -b feat/my-feature`
2. Make changes
3. Run tests: `make test` (or your test command)
4. Commit with conventional commits: `git commit -m "feat: add feature"`
5. Push and create PR

### Code Style

- Follow the language-specific style guide
- Run linter before committing: `make lint`
- Format code: `make format`

### Testing

```bash
# Run all tests
make test

# Run with coverage
make test-coverage

# Run specific test file
make test FILE=tests/test_example.py
```

## User Guides

### Quick Start

1. Access the application at `https://your-app.com`
2. Create an account or sign in
3. Follow the onboarding wizard

### Features

- **Feature A**: Description of feature A
- **Feature B**: Description of feature B
- **Feature C**: Description of feature C

### FAQ

**Q: How do I reset my password?**
A: Click "Forgot Password" on the login page.

**Q: How do I contact support?**
A: Email support@example.com or use the in-app chat.

---

*Add more guides as the project grows.*
