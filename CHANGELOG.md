# Changelog

All notable changes are listed here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and versions follow
[Semantic Versioning](https://semver.org/).

## [Unreleased]

## [1.1.1] - 2026-11-12

### Changed

- Reaches the WireGuard and NetBird networks MiniScale 1.2 can join, through its proxy (MiniDeck 1.1.0).

## [1.1.0] - 2026-11-12

### Added

- **X** on a page lists its sections and jumps to the one you pick.
- **L2**/**R2** jump to the first or last row of a list, as they already do on a page.
- Download all for offline says when the list of pages was last checked.
- When the first download of the list of pages fails, **A** tries again instead of closing MiniBook.

### Changed

- Built with Go 1.27.4, with updated dependencies.

## [1.0.3] - 2026-11-08

### Changed

- A failed save says to check that the card has free space.
- Grey and red small text is easier to read (MiniDeck 1.0.3).

## [1.0.2] - 2026-11-03

### Fixed

- A page too large to download in full is no longer saved cut off; the saved copy is kept instead.

## [1.0.1] - 2026-11-02

### Fixed

- The on-screen keyboard (Search) now shows how to leave it: B on an empty field goes back, and the footer shows B Back, or MENU Cancel once there is text (MiniDeck 1.0.1).

## [1.0.0] - 2026-11-02

### Added

- The first release: the Onion OS docs, saved for offline reading, with search, and Settings to start on the last page read and to clear the saved pages.

[Unreleased]: https://github.com/zakijss/MiniBook/compare/v1.1.1...HEAD
[1.1.1]: https://github.com/zakijss/MiniBook/compare/v1.1.0...v1.1.1
[1.1.0]: https://github.com/zakijss/MiniBook/compare/v1.0.3...v1.1.0
[1.0.3]: https://github.com/zakijss/MiniBook/compare/v1.0.2...v1.0.3
[1.0.2]: https://github.com/zakijss/MiniBook/compare/v1.0.1...v1.0.2
[1.0.1]: https://github.com/zakijss/MiniBook/compare/v1.0.0...v1.0.1
[1.0.0]: https://github.com/zakijss/MiniBook/releases/tag/v1.0.0
