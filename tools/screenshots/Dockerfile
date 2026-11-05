# Keep the tag on the playwright version in package.json. The image has the
# browser and its fonts; ffmpeg is for the tour GIF.
FROM mcr.microsoft.com/playwright:v1.64.1-noble@sha256:2f05f8a734509a7b3f26a912639488ee08f58a7b292d14d62271bcff9038768f
RUN apt-get update && apt-get install -y --no-install-recommends ffmpeg
