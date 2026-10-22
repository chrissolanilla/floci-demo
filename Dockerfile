FROM node:24-alpine AS frontend
WORKDIR /frontend
COPY frontend/package.json frontend/package-lock.json ./
RUN npm ci
COPY frontend/ ./
RUN npm run build

FROM golang:1.26-alpine AS backend
WORKDIR /src
COPY backend/go.mod backend/go.sum ./
RUN go mod download
COPY backend/ ./
RUN CGO_ENABLED=0 go build -o /app .

FROM alpine:3.23
RUN apk add --no-cache ca-certificates && adduser -D app && mkdir /data && chown app:app /data
COPY --from=backend /app /app
COPY --from=frontend /frontend/dist /frontend/dist
USER app
EXPOSE 8080
CMD ["/app"]
