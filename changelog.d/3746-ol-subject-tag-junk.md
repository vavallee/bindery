### Fixed
- **No more machine tags in a book's genre chips** (#3746). OpenLibrary mixes catalogue tags like "seriesRed Rising Trilogy", "nyt:hardcover-fiction=2015-01-25", "place:Mars" and "person:Darrow" into its subjects, and they showed up as genres on the book page. They are dropped when metadata comes in and hidden on books that already have them, and the next save stores the clean list.
