FROM python:3.12-slim
WORKDIR /app
COPY pyproject.toml README.md ./
COPY src ./src
RUN pip install --no-cache-dir . && useradd --create-home filewise && mkdir /data && chown filewise /data
USER filewise
VOLUME ["/data"]
EXPOSE 8000
ENTRYPOINT ["filewise"]
CMD ["--db", "/data/filewise.db", "serve", "--host", "0.0.0.0", "--tokens", "/run/secrets/filewise_tokens"]
