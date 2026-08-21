include .env
export

docker-up:
	sudo docker compose --env-file .env -f docker-compose.yml up -d

docker-check:
	sudo docker exec -it ${DB_NAME}-postgres psql -U ${DB_USER} -d ${DB_NAME} -c "\dt"

docker-down:
	sudo docker compose --env-file .env -f docker-compose.yml down -v

docker-logs:
	sudo docker compose --env-file .env -f docker-compose.yml logs -f

docker-app-logs:
	sudo docker run --rm -v traffic-bot-app-logs:/logs   alpine:3.20   tail -n 50 /logs/traffic-bot.log

docker-status:
	sudo docker compose ps

docker-rebuild:
	sudo docker compose --env-file .env -f docker-compose.yml build --no-cache
	sudo docker compose --env-file .env -f docker-compose.yml up -d
