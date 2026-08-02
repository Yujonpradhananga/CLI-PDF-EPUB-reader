# pdf-cli

A terminal-based PDF, EPUB, and DOCX viewer with fuzzy file search, high-resolution image rendering, auto-reload for LaTeX workflows, and intelligent text reflow.

## Screenshots

![Screenshot 1](screenshots/ss6.png)
![Screenshot 2](screenshots/ss2.png)
![Screenshot 3](screenshots/ss3.png)
![Screenshot 4](screenshots/ss4.png)
![Screenshot 5](screenshots/ss5.png)
![Screenshot 6](screenshots/ss1.png)
![Screenshot 7](screenshots/ss7.png)
![Screenshot 8](screenshots/ss8.png)
![Screenshot 9](screenshots/ss9.png)

## Features

- **Fuzzy File Search**: Interactive file picker with fuzzy search to quickly find your PDFs and EPUBs
- **Smart Content Detection**: Automatically detects and displays text, images, or mixed content pages
- **High-Resolution Image Rendering**: Uses terminal graphics protocols (Sixel/Kitty/iTerm2) for crisp image display
- **Image Invert**: Inverts the Image while preserving the core colors of the image.
- **HiDPI/Retina Support**: Dynamic cell size detection for sharp rendering on high-DPI displays
- **Auto-Reload**: Automatically reloads when the PDF changes (perfect for LaTeX compilation with `latexmk -pvc`)
- **Fit Modes**: Toggle between height-fit, width-fit, and auto-fit modes
- **Manual Zoom**: Adjust zoom from 10% to 200%
- **In-Document Search**: Search for text within documents
- **Intelligent Text Reflow**: Automatically reformats text to fit your terminal width while preserving paragraphs
- **Terminal-Aware**: Detects your terminal type and optimizes rendering accordingly
- **Multiple Formats**: Supports PDF, EPUB, and DOCX documents

## Keyboard Shortcuts

| Key | Action |
|-----|--------|
| `j` / `Space` / `Down` / `Right` | Next page |
| `k` / `Up` / `Left` | Previous page |
| `g` | Go to specific page |
| `T` | Table of contents overlay (fuzzy search, Enter jumps) |
| `b` | Back to file picker |
| `/` | Search in document |
| `n` | Next search result |
| `N` | Previous search result |
| `t` | Toggle text/image/auto mode |
| `f` | Cycle fit modes (height/width/auto) |
| `+` / `=` | Zoom in |
| `-` | Zoom out |
| `r` | Refresh display (re-detect cell size) |
| `d` | Show debug info |
| `h` | Show help |
| `q` | Quit |

## Installation

### NixOSNixOS Installation

Add the `pdf-cli.nix` file to your nixos configuration directory, then add the overlay to your `flake.nix`:

```nix
let
  myOverlay = final: prev: {
    pdf-cli = prev.callPackage ./pdf-cli.nix { };
  };
in
```

Then apply the overlay in your `nixosConfigurations`:

```nix
({ config, pkgs, ... }: {
  nixpkgs.overlays = [ myOverlay ];
})
```

Then in your `home.nix`:

```nix
home.packages = with pkgs; [
  pdf-cli
];
```

### Building from source

```bash
# Clone this repository
git clone https://github.com/Yujonpradhananga/pdf-cli
cd pdf-cli

# Install dependencies
go mod tidy

# Build
go build -o pdf-cli .

# Optionally move to your PATH
mv pdf-cli ~/local/bin/
```

### Usage

```bash
# Search current directory (default)
pdf-cli

# Search specific directory
pdf-cli ~/Documents/papers/

# Open a specific file directly
pdf-cli paper.pdf
```

## LaTeX Workflow

The auto-reload feature makes this viewer ideal for LaTeX editing:

1. Open your PDF: `pdf-cli paper.pdf`
2. Run LaTeX compiler in another terminal: `latexmk -pvc paper.tex`
3. The viewer automatically reloads when the PDF updates, preserving your page position

The viewer handles partially-written PDFs gracefully, waiting for the file to stabilize before reloading.

## Dependencies

- Go 1.21+
- [go-fitz](https://github.com/gen2brain/go-fitz) - PDF/EPUB parsing (MuPDF)
- [go-termimg](https://github.com/blacktop/go-termimg) - Terminal image rendering
- [fuzzy](https://github.com/sahilm/fuzzy) - Fuzzy search
- [golang.org/x/term](https://golang.org/x/term) - Terminal control

## Supported Terminals

Optimized for terminals with graphics support:

- **Kitty** (recommended) - Native cell size detection via escape sequences
- WezTerm
- iTerm2
- Alacritty
- Foot
- xterm (with Sixel support)

Works in any terminal, but image rendering quality depends on terminal capabilities.

## How It Works

The reader scans the current directory (or specified directory) for PDF, EPUB, and DOCX files. Use the fuzzy search to quickly filter and select a file. The viewer intelligently detects whether pages contain text, images, or both, and renders them appropriately for terminal display.

PDFs are rendered as images by default (essential for math, diagrams, and formatted content) at a DPI calculated to match your terminal's pixel dimensions for optimal sharpness.

## License

MIT

---

By [Yujon Pradhananga](https://github.com/Yujonpradhananga)
