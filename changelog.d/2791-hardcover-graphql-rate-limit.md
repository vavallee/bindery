### Fixed
- A Hardcover rate limit that arrives as a normal answer carrying an error message now slows Bindery down instead of speeding it up (#2791). It used to count as a healthy request, which relaxed the pacing earlier refusals had set up. It is now retried and paced like any other refusal, and other query errors no longer count toward relaxing the pacing either.
