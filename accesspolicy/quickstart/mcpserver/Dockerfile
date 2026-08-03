FROM node:20-slim

WORKDIR /app
RUN npm install -g @modelcontextprotocol/server-everything

EXPOSE 3001

CMD ["mcp-server-everything", "sse"]
